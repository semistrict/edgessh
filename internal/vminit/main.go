//go:build linux

// edgessh-init runs as PID 1 inside Firecracker VMs.
// It mounts essential filesystems, injects the SSH public key from the kernel
// command line, starts sshd, and execs bash on the serial console.
package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func main() {
	mount("proc", "/proc", "proc", 0)
	mount("sysfs", "/sys", "sysfs", 0)
	mount("devtmpfs", "/dev", "devtmpfs", 0)
	mkdirAll("/dev/pts", "/dev/shm", "/run")
	mount("devpts", "/dev/pts", "devpts", 0)
	mount("tmpfs", "/dev/shm", "tmpfs", 0)
	mount("tmpfs", "/run", "tmpfs", 0)

	setHostname("edgessh-vm")

	if pubKey := pubKeyFromCmdline(); pubKey != "" {
		injectAuthorizedKeys(pubKey)
	} else {
		fmt.Println("WARNING: no edgessh_pubkey in kernel cmdline")
	}

	startSSHD()

	fmt.Println("edgessh-vm ready")

	// Stay alive as PID 1. Users interact via SSH, not the serial console.
	// Reap zombie children (init responsibility).
	for {
		var status syscall.WaitStatus
		syscall.Wait4(-1, &status, 0, nil)
	}
}

func mount(source, target, fstype string, flags uintptr) {
	os.MkdirAll(target, 0o755)
	if err := syscall.Mount(source, target, fstype, flags, ""); err != nil {
		// devtmpfs may already be mounted by the kernel
		if !os.IsExist(err) {
			fmt.Printf("mount %s on %s: %v\n", fstype, target, err)
		}
	}
}

func mkdirAll(paths ...string) {
	for _, p := range paths {
		os.MkdirAll(p, 0o755)
	}
}

func setHostname(name string) {
	syscall.Sethostname([]byte(name))
}

// pubKeyFromCmdline reads /proc/cmdline and extracts the base64-encoded
// public key from the edgessh_pubkey=<base64> parameter.
func pubKeyFromCmdline() string {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return ""
	}

	for _, param := range strings.Fields(string(data)) {
		if strings.HasPrefix(param, "edgessh_pubkey=") {
			encoded := strings.TrimPrefix(param, "edgessh_pubkey=")
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				fmt.Printf("failed to decode pubkey: %v\n", err)
				return ""
			}
			return strings.TrimSpace(string(decoded))
		}
	}
	return ""
}

func injectAuthorizedKeys(pubKey string) {
	os.Chmod("/root", 0o700)
	os.MkdirAll("/root/.ssh", 0o700)
	if err := os.WriteFile("/root/.ssh/authorized_keys", []byte(pubKey+"\n"), 0o600); err != nil {
		fmt.Printf("failed to write authorized_keys: %v\n", err)
		return
	}
	fmt.Printf("authorized_keys: %s\n", pubKey)
}

func startSSHD() {
	os.MkdirAll("/run/sshd", 0o755)

	cmd := exec.Command("/usr/sbin/sshd",
		"-o", "StrictModes=no",
		"-o", "PermitRootLogin=yes",
		"-E", "/dev/stderr",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("sshd failed to start: %v\n", err)
		return
	}
	fmt.Println("sshd started on port 22")
}
