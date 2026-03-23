package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anthropics/edgessh/internal/cfapi"
	"github.com/anthropics/edgessh/internal/config"
	"github.com/anthropics/edgessh/internal/sshclient"
	"github.com/anthropics/edgessh/internal/tunnel"
	"github.com/anthropics/edgessh/internal/workerapi"
	"golang.org/x/crypto/ssh"
)

// ensureRunning wakes the container if it's not already running.
func ensureRunning(cf *cfapi.Client, wc *workerapi.Client, cfg *config.Config, name string) error {
	resp, err := cf.ListInstances(cfg.ApplicationID)
	if err != nil {
		return err
	}

	for _, do := range resp.DurableObjects {
		if do.Name == name && do.DeploymentID != "" {
			// Check if the linked instance is running
			for _, inst := range resp.Instances {
				if inst.ID == do.DeploymentID {
					if inst.CurrentPlacement != nil && inst.CurrentPlacement.Status != nil {
						if inst.CurrentPlacement.Status.ContainerStatus == "running" {
							return nil
						}
					}
				}
			}
		}
	}

	fmt.Printf("Container %q not running, waking...\n", name)
	if err := wc.WakeContainer(name); err != nil {
		return err
	}

	// Wait for it to become available
	for i := 0; i < 30; i++ {
		time.Sleep(5 * time.Second)
		if _, err := cf.ResolveInstanceID(cfg.ApplicationID, name); err == nil {
			return nil
		}
		fmt.Print(".")
	}
	return fmt.Errorf("timed out waiting for container %q to start", name)
}

// dial establishes an SSH client connection to a named container,
// going through the Cloudflare WebSocket tunnel.
// It auto-wakes the container if it's not running.
func dial(cfg *config.Config, name string) (*ssh.Client, error) {
	cf := cfapi.NewClient(cfg)
	wc := workerapi.NewClient(cfg.WorkerURL, cfg.SessionToken)

	if err := ensureRunning(cf, wc, cfg, name); err != nil {
		return nil, err
	}

	// Retry loop — the container may need a moment after waking before
	// the SSH tunnel is ready (rollout propagation, container init, etc.)
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			fmt.Fprintf(os.Stderr, "Retrying SSH connection (%d/5)...\n", attempt+1)
			time.Sleep(3 * time.Second)
		}

		instanceID, err := cf.ResolveInstanceID(cfg.ApplicationID, name)
		if err != nil {
			lastErr = err
			continue
		}

		tunnelCreds, err := cf.GetSSHTunnel(instanceID)
		if err != nil {
			lastErr = err
			continue
		}

		conn, err := tunnel.Dial(tunnelCreds.URL, tunnelCreds.Token)
		if err != nil {
			lastErr = fmt.Errorf("WebSocket dial: %w", err)
			continue
		}

		client, err := sshclient.Connect(conn)
		if err != nil {
			conn.Close()
			lastErr = err
			continue
		}

		return client, nil
	}
	return nil, lastErr
}

func startContainerKeepalive(ctx context.Context, workerURL, doName, sessionToken string) {
	if workerURL == "" || doName == "" {
		return
	}

	base := strings.TrimRight(workerURL, "/")
	url := base + "/" + doName + "/health"
	client := &http.Client{Timeout: 10 * time.Second}

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				if err != nil {
					continue
				}
				if sessionToken != "" {
					req.Header.Set("Authorization", "Bearer "+sessionToken)
				}
				resp, err := client.Do(req)
				if err == nil && resp != nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}
		}
	}()
}
