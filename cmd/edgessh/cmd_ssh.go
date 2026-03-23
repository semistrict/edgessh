package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anthropics/edgessh/internal/config"
	"github.com/anthropics/edgessh/internal/sshclient"
	"github.com/anthropics/edgessh/internal/tunnel"
	"github.com/anthropics/edgessh/internal/workerapi"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
)

func sshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh VM_NAME [cmd [args...]]",
		Short: "SSH into a Firecracker VM",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cfg, err := requireWorkerAccess()
			if err != nil {
				return err
			}
			pubKey, err := config.ReadPublicKey()
			if err != nil {
				return fmt.Errorf("reading SSH public key: %w", err)
			}

			wc := workerapi.NewClient(cfg.WorkerURL, cfg.SessionToken)
			info, err := wc.GetVMSSHInfo(name, strings.TrimSpace(pubKey))
			if err != nil {
				return fmt.Errorf("VM %q: %w", name, err)
			}

			wsURL, err := workerWebSocketURL(cfg.WorkerURL, "/api/vm/tcp", map[string]string{
				"name":       name,
				"port":       "22",
				"ssh_pubkey": strings.TrimSpace(pubKey),
			})
			if err != nil {
				return err
			}
			var client *ssh.Client
			var lastErr error
			for attempt := 0; attempt < 5; attempt++ {
				if attempt > 0 {
					fmt.Fprintf(os.Stderr, "Retrying SSH connection (%d/5)...\n", attempt+1)
					time.Sleep(4 * time.Second)
				}
				headers := http.Header{"User-Agent": []string{"edgessh"}}
				headers.Set("Authorization", "Bearer "+cfg.SessionToken)
				conn, dialErr := tunnel.DialWithHeaders(wsURL, headers)
				if dialErr != nil {
					lastErr = fmt.Errorf("VM WebSocket dial: %w", dialErr)
					continue
				}
				client, lastErr = sshclient.ConnectVM(conn)
				if lastErr != nil {
					conn.Close()
					continue
				}
				break
			}
			if client == nil {
				return fmt.Errorf("could not connect to VM %q after 5 attempts: %w", name, lastErr)
			}
			defer client.Close()

			keepaliveCtx, cancelKeepalive := context.WithCancel(context.Background())
			defer cancelKeepalive()
			startContainerKeepalive(keepaliveCtx, cfg.WorkerURL, info.DOName, cfg.SessionToken)

			if len(args) > 1 {
				return sshclient.Exec(client, strings.Join(args[1:], " "))
			}
			return sshclient.Shell(client)
		},
	}
}

func scpCmd() *cobra.Command {
	var recursive bool

	cmd := &cobra.Command{
		Use:   "scp [-R] SRC DST",
		Short: "Copy files to/from a container",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, dst := args[0], args[1]

			// Parse INSTANCE_NAME:/path
			var name, remotePath, localPath string
			var download bool

			if parts := strings.SplitN(src, ":", 2); len(parts) == 2 && !strings.HasPrefix(src, "/") {
				name, remotePath = parts[0], parts[1]
				localPath = dst
				download = true
			} else if parts := strings.SplitN(dst, ":", 2); len(parts) == 2 && !strings.HasPrefix(dst, "/") {
				name, remotePath = parts[0], parts[1]
				localPath = src
				download = false
			} else {
				return fmt.Errorf("one of SRC or DST must be INSTANCE_NAME:/path")
			}

			cfg, err := requireWorkerAccess()
			if err != nil {
				return err
			}

			client, err := dial(cfg, name)
			if err != nil {
				return err
			}
			defer client.Close()

			if download {
				f, err := os.Create(localPath)
				if err != nil {
					return err
				}
				defer f.Close()

				if recursive {
					return sshclient.Exec(client, fmt.Sprintf("tar -cf - -C %q .", remotePath))
				}
				return sshclient.Download(client, remotePath, f)
			}

			// Upload
			f, err := os.Open(localPath)
			if err != nil {
				return err
			}
			defer f.Close()

			info, err := f.Stat()
			if err != nil {
				return err
			}

			if recursive && info.IsDir() {
				return sshclient.Exec(client, fmt.Sprintf("tar -xf - -C %q", remotePath))
			}
			return sshclient.Upload(client, f, remotePath, info.Size())
		},
	}

	cmd.Flags().BoolVarP(&recursive, "recursive", "R", false, "Recursively copy directories")
	return cmd
}

func exposeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "expose INSTANCE_NAME PORT",
		Short: "Expose a port from a container via SSH port forwarding",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, portStr := args[0], args[1]

			cfg, err := requireWorkerAccess()
			if err != nil {
				return err
			}

			client, err := dial(cfg, name)
			if err != nil {
				return err
			}
			defer client.Close()

			fmt.Printf("Forwarding localhost:%s -> %s:%s\n", portStr, name, portStr)
			fmt.Println("Press Ctrl+C to stop")

			listener, err := client.Listen("tcp", "127.0.0.1:"+portStr)
			if err != nil {
				return fmt.Errorf("remote listen: %w", err)
			}
			defer listener.Close()

			// Actually we want local forwarding: listen locally, forward to remote
			// ssh.Client doesn't have local forwarding built in, let's do it manually
			return localForward(client, portStr)
		},
	}
}

func localForward(client *ssh.Client, port string) error {
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		return err
	}
	defer ln.Close()

	for {
		local, err := ln.Accept()
		if err != nil {
			return err
		}

		remote, err := client.Dial("tcp", "127.0.0.1:"+port)
		if err != nil {
			local.Close()
			continue
		}

		go func() {
			defer local.Close()
			defer remote.Close()
			go io.Copy(remote, local)
			io.Copy(local, remote)
		}()
	}
}
