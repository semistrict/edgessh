//go:build linux

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	fcDir      = "/var/lib/firecracker"
	fcBin      = fcDir + "/firecracker"
	kernelPath = fcDir + "/vmlinux"
)

type VM struct {
	Name     string `json:"name"`
	SubnetID int    `json:"subnet_id"`
	PID      int    `json:"pid,omitempty"`
	Socket   string `json:"socket"`
	GuestIP  string `json:"guest_ip"`
	Status   string `json:"status"`

	cmd *exec.Cmd
}

type VMManager struct {
	mu     sync.Mutex
	vms    map[string]*VM
	nextID int
	r2URL  string
}

func NewVMManager(r2URL string) *VMManager {
	return &VMManager{
		vms:    make(map[string]*VM),
		nextID: 1,
		r2URL:  r2URL,
	}
}

const vmKeyPath = "/etc/edgessh/vm_key"

// ensureKeyPair generates an ed25519 keypair for SSH into VMs if it doesn't exist.
// Returns the public key string.
func (m *VMManager) ensureKeyPair() (string, error) {
	pubPath := vmKeyPath + ".pub"
	if data, err := os.ReadFile(pubPath); err == nil {
		return strings.TrimSpace(string(data)), nil
	}

	os.MkdirAll(filepath.Dir(vmKeyPath), 0o700)
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", vmKeyPath, "-N", "", "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ssh-keygen: %w\n%s", err, string(out))
	}

	data, err := os.ReadFile(pubPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func (m *VMManager) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/vm/setup", m.handleSetup)
	mux.HandleFunc("/vm/create", m.handleCreate)
	mux.HandleFunc("/vm/list", m.handleList)
	mux.HandleFunc("/vm/stop", m.handleStop)
	mux.HandleFunc("/vm/logs", m.handleLogs)
}

func (m *VMManager) handleLogs(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "missing name parameter", http.StatusBadRequest)
		return
	}
	logPath := fmt.Sprintf("/tmp/%s.log", name)
	data, err := os.ReadFile(logPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Write(data)
}

func (m *VMManager) handleSetup(w http.ResponseWriter, r *http.Request) {
	if err := m.ensureAssets(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "ok\n")
}

func (m *VMManager) assetsReady() bool {
	for _, path := range []string{kernelPath, fcBin, fcDir + "/rootfs.ext4"} {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}

func (m *VMManager) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !m.assetsReady() {
		http.Error(w, "assets not downloaded — run /vm/setup first", http.StatusPreconditionFailed)
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		name = fmt.Sprintf("vm%d", m.nextID)
	}

	pubKey, err := m.ensureKeyPair()
	if err != nil {
		http.Error(w, "generating keypair: "+err.Error(), http.StatusInternalServerError)
		return
	}

	m.mu.Lock()
	if _, exists := m.vms[name]; exists {
		m.mu.Unlock()
		http.Error(w, fmt.Sprintf("VM %q already exists", name), http.StatusConflict)
		return
	}
	subnetID := m.nextID
	m.nextID++
	m.mu.Unlock()

	vm, err := m.startVM(name, subnetID, pubKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(vm)
}

func (m *VMManager) handleList(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	vms := make([]*VM, 0, len(m.vms))
	for _, vm := range m.vms {
		vms = append(vms, vm)
	}
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(vms)
}

// Shutdown gracefully stops all VMs: sends SIGTERM and waits for them to exit.
func (m *VMManager) Shutdown() {
	m.mu.Lock()
	vms := make([]*VM, 0, len(m.vms))
	for _, vm := range m.vms {
		vms = append(vms, vm)
	}
	m.mu.Unlock()

	if len(vms) == 0 {
		return
	}

	fmt.Printf("Shutting down %d VM(s)...\n", len(vms))

	for _, vm := range vms {
		if vm.cmd != nil && vm.cmd.Process != nil {
			fmt.Printf("  SIGTERM → %s (pid %d)\n", vm.Name, vm.cmd.Process.Pid)
			vm.cmd.Process.Signal(syscall.SIGTERM)
		}
	}

	for _, vm := range vms {
		if vm.cmd != nil {
			vm.cmd.Wait()
			fmt.Printf("  %s exited\n", vm.Name)
		}
		cleanupNetns(vm.Name, vm.SubnetID)
	}

	fmt.Println("All VMs stopped")
}

func (m *VMManager) handleStop(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "missing name parameter", http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	vm, exists := m.vms[name]
	if !exists {
		m.mu.Unlock()
		http.Error(w, fmt.Sprintf("VM %q not found", name), http.StatusNotFound)
		return
	}
	delete(m.vms, name)
	m.mu.Unlock()

	if vm.cmd != nil && vm.cmd.Process != nil {
		vm.cmd.Process.Kill()
	}
	cleanupNetns(name, vm.SubnetID)
	os.Remove(vm.Socket)
	os.Remove(rootfsPath(name))

	fmt.Fprintf(w, "VM %q stopped\n", name)
}

func (m *VMManager) startVM(name string, subnetID int, pubKey string) (*VM, error) {
	t0 := time.Now()
	log := func(step string) {
		fmt.Printf("[vm/%s] %s: %s\n", name, step, time.Since(t0).Round(time.Millisecond))
	}

	src := fcDir + "/rootfs.ext4"
	dst := rootfsPath(name)
	if err := copyFile(src, dst); err != nil {
		return nil, fmt.Errorf("copying rootfs: %w", err)
	}
	log("rootfs copied")

	log("rootfs ready")

	if err := setupNetns(name, subnetID); err != nil {
		os.Remove(dst)
		return nil, fmt.Errorf("setting up network: %w", err)
	}
	log("network ready")

	sock := fmt.Sprintf("/tmp/%s.sock", name)
	os.Remove(sock)

	logPath := fmt.Sprintf("/tmp/%s.log", name)
	logFile, err := os.Create(logPath)
	if err != nil {
		cleanupNetns(name, subnetID)
		os.Remove(dst)
		return nil, fmt.Errorf("creating log file: %w", err)
	}

	cmd := exec.Command("ip", "netns", "exec", name, fcBin, "--api-sock", sock)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		cleanupNetns(name, subnetID)
		os.Remove(dst)
		return nil, fmt.Errorf("starting firecracker: %w", err)
	}
	log("firecracker process started")

	for i := 0; i < 20; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	log("socket ready")

	// Guest is always on 172.16.0.0/24 inside the netns (tap0)
	guestIP := "172.16.0.2"
	gwIP := "172.16.0.1"

	if err := configureVM(sock, name, subnetID, guestIP, gwIP, pubKey); err != nil {
		cmd.Process.Kill()
		cleanupNetns(name, subnetID)
		os.Remove(dst)
		os.Remove(sock)
		return nil, fmt.Errorf("configuring VM: %w", err)
	}
	log("configured")

	if err := fcAPI(sock, "PUT", "/actions", map[string]string{"action_type": "InstanceStart"}); err != nil {
		cmd.Process.Kill()
		cleanupNetns(name, subnetID)
		os.Remove(dst)
		os.Remove(sock)
		return nil, fmt.Errorf("starting VM: %w", err)
	}
	log("VM started")

	vm := &VM{
		Name:     name,
		SubnetID: subnetID,
		PID:      cmd.Process.Pid,
		Socket:   sock,
		GuestIP:  guestIP,
		Status:   "running",
		cmd:      cmd,
	}

	m.mu.Lock()
	m.vms[name] = vm
	m.mu.Unlock()

	go func() {
		cmd.Wait()
		m.mu.Lock()
		if v, ok := m.vms[name]; ok && v == vm {
			v.Status = "stopped"
		}
		m.mu.Unlock()
	}()

	return vm, nil
}

func configureVM(sock, name string, subnetID int, guestIP, gwIP, pubKey string) error {
	// Encode pubkey as base64 for kernel cmdline (no spaces/special chars)
	encodedKey := base64.StdEncoding.EncodeToString([]byte(pubKey))
	bootArgs := fmt.Sprintf("console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw init=/edgessh-init ip=%s::%s:255.255.255.0::eth0:off edgessh_name=%s edgessh_pubkey=%s", guestIP, gwIP, name, encodedKey)

	if err := fcAPI(sock, "PUT", "/boot-source", map[string]string{
		"kernel_image_path": kernelPath,
		"boot_args":         bootArgs,
	}); err != nil {
		return fmt.Errorf("boot-source: %w", err)
	}

	if err := fcAPI(sock, "PUT", "/drives/rootfs", map[string]interface{}{
		"drive_id":       "rootfs",
		"path_on_host":   rootfsPath(name),
		"is_root_device": true,
		"is_read_only":   false,
	}); err != nil {
		return fmt.Errorf("drives: %w", err)
	}

	mac := fmt.Sprintf("AA:FC:00:00:00:%02x", subnetID)
	if err := fcAPI(sock, "PUT", "/network-interfaces/eth0", map[string]string{
		"iface_id":      "eth0",
		"guest_mac":     mac,
		"host_dev_name": "tap0",
	}); err != nil {
		return fmt.Errorf("network: %w", err)
	}

	if err := fcAPI(sock, "PUT", "/machine-config", map[string]interface{}{
		"vcpu_count":   1,
		"mem_size_mib": 128,
	}); err != nil {
		return fmt.Errorf("machine-config: %w", err)
	}

	return nil
}

func fcAPI(sock, method, path string, body interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
	}

	req, err := http.NewRequest(method, "http://localhost"+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("firecracker API %s %s: %d %s", method, path, resp.StatusCode, string(body))
	}

	return nil
}

func (m *VMManager) ensureAssets() error {
	os.MkdirAll(fcDir, 0o755)

	simpleAssets := map[string]string{
		kernelPath: m.r2URL + "/vmlinux",
		fcBin:      m.r2URL + "/firecracker",
	}
	for path, url := range simpleAssets {
		if _, err := os.Stat(path); err == nil {
			continue
		}
		fmt.Printf("Downloading %s...\n", filepath.Base(path))
		if err := download(url, path); err != nil {
			return fmt.Errorf("downloading %s: %w", filepath.Base(path), err)
		}
	}
	os.Chmod(fcBin, 0o755)

	rootfs := fcDir + "/rootfs.ext4"
	if _, err := os.Stat(rootfs); err != nil {
		fmt.Println("Downloading rootfs.ext4.gz...")
		if err := downloadGunzip(m.r2URL+"/rootfs.ext4.gz", rootfs); err != nil {
			return fmt.Errorf("downloading rootfs: %w", err)
		}
	}

	return nil
}

func downloadGunzip(url, dst string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, gz)
	return err
}

func download(url, dst string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

func rootfsPath(name string) string {
	return fmt.Sprintf("%s/rootfs-%s.ext4", fcDir, name)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
