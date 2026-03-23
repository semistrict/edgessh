//go:build linux

// Package main implements a generic container sidecar proxy.
// It has zero domain knowledge — no awareness of Firecracker, loophole, VMs,
// SSH keys, or boot arguments. All commands and paths come from the caller.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anthropics/edgessh/internal/tunnel"
	"github.com/gorilla/websocket"
)

// TrackedProc is a background process tracked for SIGTERM cleanup.
type TrackedProc struct {
	Name string
	Cmd  *exec.Cmd
}

// Proxy exposes low-level OS primitives over HTTP.
type Proxy struct {
	mu    sync.Mutex
	procs map[int]*TrackedProc
}

func NewProxy() *Proxy {
	return &Proxy{procs: make(map[int]*TrackedProc)}
}

func (p *Proxy) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/exec", p.handleExec)
	mux.HandleFunc("/proc/start", p.handleProcStart)
	mux.HandleFunc("/proc/signal", p.handleProcSignal)
	mux.HandleFunc("/proc/wait", p.handleProcWait)
	mux.HandleFunc("/netns/setup", p.handleNetnsSetup)
	mux.HandleFunc("/netns/cleanup", p.handleNetnsCleanup)
	mux.HandleFunc("/fc", p.handleFC)
	mux.HandleFunc("/tcp", p.handleTCP)
	mux.HandleFunc("/info", p.handleInfo)
	mux.HandleFunc("/uds", p.handleUDS)
}

// Shutdown sends SIGTERM to all tracked processes, waits, then SIGKILLs stragglers.
func (p *Proxy) Shutdown() {
	p.mu.Lock()
	procs := make([]*TrackedProc, 0, len(p.procs))
	for _, proc := range p.procs {
		procs = append(procs, proc)
	}
	p.mu.Unlock()

	if len(procs) == 0 {
		return
	}

	slog.Info("shutting down tracked processes", "count", len(procs))
	for _, proc := range procs {
		if proc.Cmd.Process != nil {
			proc.Cmd.Process.Signal(syscall.SIGTERM)
		}
	}

	for _, proc := range procs {
		if proc.Cmd.Process == nil {
			continue
		}
		done := make(chan struct{})
		go func() {
			proc.Cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
			slog.Info("process exited", "pid", proc.Cmd.Process.Pid, "name", proc.Name)
		case <-time.After(10 * time.Second):
			slog.Warn("killing process after timeout", "pid", proc.Cmd.Process.Pid, "name", proc.Name)
			proc.Cmd.Process.Kill()
			<-done
		}
	}
	slog.Info("all tracked processes stopped")
}

// POST /exec — run a command synchronously, return output.
func (p *Proxy) handleExec(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cmd []string `json:"cmd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Cmd) == 0 {
		http.Error(w, "empty cmd", http.StatusBadRequest)
		return
	}

	cmd := exec.Command(req.Cmd[0], req.Cmd[1:]...)
	stdout, err := cmd.Output()
	exitCode := 0
	stderr := ""
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
			stderr = string(exitErr.Stderr)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	jsonReply(w, map[string]interface{}{
		"stdout":    string(stdout),
		"stderr":    stderr,
		"exit_code": exitCode,
	})
}

// POST /proc/start — start a background process, return PID.
func (p *Proxy) handleProcStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cmd     []string `json:"cmd"`
		Name    string   `json:"name"`
		LogFile string   `json:"log_file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Cmd) == 0 {
		http.Error(w, "empty cmd", http.StatusBadRequest)
		return
	}

	cmd := exec.Command(req.Cmd[0], req.Cmd[1:]...)
	if req.LogFile != "" {
		f, err := os.Create(req.LogFile)
		if err != nil {
			http.Error(w, fmt.Sprintf("creating log file: %v", err), http.StatusInternalServerError)
			return
		}
		cmd.Stdout = f
		cmd.Stderr = f
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Start(); err != nil {
		http.Error(w, fmt.Sprintf("starting process: %v", err), http.StatusInternalServerError)
		return
	}

	pid := cmd.Process.Pid
	p.mu.Lock()
	p.procs[pid] = &TrackedProc{Name: req.Name, Cmd: cmd}
	p.mu.Unlock()

	go func() {
		cmd.Wait()
		p.mu.Lock()
		delete(p.procs, pid)
		p.mu.Unlock()
	}()

	slog.Info("process started", "pid", pid, "name", req.Name, "cmd", req.Cmd)
	jsonReply(w, map[string]interface{}{"pid": pid})
}

// POST /proc/signal — send a signal to a tracked process.
func (p *Proxy) handleProcSignal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PID    int    `json:"pid"`
		Signal string `json:"signal"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	p.mu.Lock()
	proc, exists := p.procs[req.PID]
	p.mu.Unlock()
	if !exists {
		http.Error(w, fmt.Sprintf("pid %d not tracked", req.PID), http.StatusNotFound)
		return
	}

	var sig syscall.Signal
	switch strings.ToUpper(req.Signal) {
	case "TERM", "SIGTERM":
		sig = syscall.SIGTERM
	case "KILL", "SIGKILL":
		sig = syscall.SIGKILL
	case "INT", "SIGINT":
		sig = syscall.SIGINT
	default:
		http.Error(w, fmt.Sprintf("unknown signal %q", req.Signal), http.StatusBadRequest)
		return
	}

	if err := proc.Cmd.Process.Signal(sig); err != nil {
		http.Error(w, fmt.Sprintf("signal: %v", err), http.StatusInternalServerError)
		return
	}
	jsonReply(w, map[string]interface{}{"signaled": true})
}

// POST /proc/wait — wait for a tracked process to exit (with timeout).
func (p *Proxy) handleProcWait(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PID       int `json:"pid"`
		TimeoutMs int `json:"timeout_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	p.mu.Lock()
	proc, exists := p.procs[req.PID]
	p.mu.Unlock()
	if !exists {
		jsonReply(w, map[string]interface{}{"exited": true, "exit_code": -1})
		return
	}

	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	done := make(chan struct{})
	go func() {
		proc.Cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		exitCode := 0
		if proc.Cmd.ProcessState != nil {
			exitCode = proc.Cmd.ProcessState.ExitCode()
		}
		jsonReply(w, map[string]interface{}{"exited": true, "exit_code": exitCode})
	case <-time.After(timeout):
		jsonReply(w, map[string]interface{}{"exited": false})
	}
}

// POST /netns/setup — create a network namespace with veth + tap + NAT.
func (p *Proxy) handleNetnsSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		SubnetID int    `json:"subnet_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}

	if err := setupNetns(req.Name, req.SubnetID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonReply(w, map[string]interface{}{"ok": true})
}

// POST /netns/cleanup — remove a network namespace.
func (p *Proxy) handleNetnsCleanup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		SubnetID int    `json:"subnet_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cleanupNetns(req.Name, req.SubnetID)
	jsonReply(w, map[string]interface{}{"ok": true})
}

// POST /fc — proxy a request to a Firecracker unix socket.
func (p *Proxy) handleFC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Socket string      `json:"socket"`
		Method string      `json:"method"`
		Path   string      `json:"path"`
		Body   interface{} `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Socket == "" || req.Method == "" || req.Path == "" {
		http.Error(w, "missing socket, method, or path", http.StatusBadRequest)
		return
	}

	data, err := json.Marshal(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", req.Socket)
			},
		},
	}

	httpReq, err := http.NewRequest(req.Method, "http://localhost"+req.Path, bytes.NewReader(data))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		http.Error(w, fmt.Sprintf("firecracker %s %s: %d %s", req.Method, req.Path, resp.StatusCode, string(respBody)), http.StatusBadGateway)
		return
	}
	jsonReply(w, map[string]interface{}{"ok": true})
}

// GET /tcp?name=NETNS&address=IP:PORT — WebSocket-to-TCP proxy in a network namespace.
func (p *Proxy) handleTCP(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	address := r.URL.Query().Get("address")
	if address == "" {
		port := r.URL.Query().Get("port")
		if port == "" {
			http.Error(w, "missing address or port", http.StatusBadRequest)
			return
		}
		if _, err := strconv.Atoi(port); err != nil {
			http.Error(w, "invalid port", http.StatusBadRequest)
			return
		}
		address = net.JoinHostPort("172.16.0.2", port)
	}

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	target, err := dialInNetns(name, address)
	if err != nil {
		ws.Close()
		return
	}

	wsConn := tunnel.Wrap(ws)
	go func() {
		defer wsConn.Close()
		defer target.Close()
		io.Copy(target, wsConn)
	}()
	defer wsConn.Close()
	defer target.Close()
	io.Copy(wsConn, target)
}

// GET /info — return container system information.
func (p *Proxy) handleInfo(w http.ResponseWriter, r *http.Request) {
	jsonReply(w, map[string]interface{}{
		"memory_mib": containerMemMiB(),
	})
}

// containerMemMiB returns the container's memory limit in MiB.
func containerMemMiB() int {
	// cgroup v2
	if data, err := os.ReadFile("/sys/fs/cgroup/memory.max"); err == nil {
		s := strings.TrimSpace(string(data))
		if s != "max" {
			if b, err := strconv.ParseInt(s, 10, 64); err == nil {
				return int(b / (1024 * 1024))
			}
		}
	}
	// cgroup v1
	if data, err := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err == nil {
		s := strings.TrimSpace(string(data))
		if b, err := strconv.ParseInt(s, 10, 64); err == nil {
			if b < 256*1024*1024*1024 {
				return int(b / (1024 * 1024))
			}
		}
	}
	// /proc/meminfo fallback
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
						return int(kb / 1024)
					}
				}
			}
		}
	}
	return 2048
}

// GET /uds?socket=PATH&path=/metrics — proxy a GET request to a unix domain socket.
// Returns the raw response body with the original content type.
func (p *Proxy) handleUDS(w http.ResponseWriter, r *http.Request) {
	sock := r.URL.Query().Get("socket")
	if sock == "" {
		http.Error(w, "missing socket parameter", http.StatusBadRequest)
		return
	}
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		reqPath = "/"
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
	}

	resp, err := client.Get("http://localhost" + reqPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("unix socket request failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func jsonReply(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
