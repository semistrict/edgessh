//go:build linux

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func main() {
	r2URL := os.Getenv("R2_URL")
	if r2URL == "" {
		r2URL = "https://pub-87ad5457c58141ecb98d7784edb7d55d.r2.dev"
	}

	vmm := NewVMManager(r2URL)

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok\n")
	})

	mux.HandleFunc("/file", handleFile)

	vmm.RegisterRoutes(mux)

	fmt.Println("edgessh-noded listening on :8080")
	go http.ListenAndServe(":8080", mux)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	fmt.Println("edgessh-noded shutting down")
}

func handleFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}
	path = filepath.Clean(path)

	switch r.Method {
	case http.MethodGet:
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		defer f.Close()

		info, _ := f.Stat()
		if info != nil && info.IsDir() {
			entries, err := os.ReadDir(path)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			for _, e := range entries {
				prefix := "-"
				if e.IsDir() {
					prefix = "d"
				}
				fmt.Fprintf(w, "%s %s\n", prefix, e.Name())
			}
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		io.Copy(w, f)

	case http.MethodPut:
		dir := filepath.Dir(path)
		os.MkdirAll(dir, 0o755)

		f, err := os.Create(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()

		n, err := io.Copy(f, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if strings.Contains(r.Header.Get("X-Edgessh-Mode"), "x") {
			os.Chmod(path, 0o755)
		}

		fmt.Fprintf(w, "wrote %d bytes to %s\n", n, path)

	case http.MethodDelete:
		if err := os.RemoveAll(path); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "deleted %s\n", path)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
