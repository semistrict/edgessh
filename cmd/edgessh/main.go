package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/anthropics/edgessh/internal/api"
	"github.com/anthropics/edgessh/internal/auth"
	"github.com/anthropics/edgessh/internal/config"
	"github.com/anthropics/edgessh/internal/sshclient"
	"github.com/anthropics/edgessh/internal/tunnel"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
)

func main() {
	root := &cobra.Command{
		Use:           "edgessh",
		Short:         "SSH into Cloudflare Containers",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(loginCmd())
	root.AddCommand(setupCmd())
	root.AddCommand(createCmd())
	root.AddCommand(listCmd())
	root.AddCommand(sshCmd())
	root.AddCommand(stopCmd())
	root.AddCommand(exposeCmd())
	root.AddCommand(scpCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func requireSetup() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if cfg.ApplicationID == "" || cfg.WorkerURL == "" {
		return nil, fmt.Errorf("run 'edgessh setup' first")
	}
	return cfg, nil
}

// ensureRunning wakes the container if it's not already running.
func ensureRunning(apiClient *api.Client, cfg *config.Config, name string) error {
	resp, err := apiClient.ListInstances(cfg.ApplicationID)
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
	if err := apiClient.WakeContainer(cfg.WorkerURL, name); err != nil {
		return err
	}

	// Wait for it to become available
	for i := 0; i < 30; i++ {
		time.Sleep(5 * time.Second)
		if _, err := apiClient.ResolveInstanceID(cfg.ApplicationID, name); err == nil {
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
	apiClient := api.NewClient(cfg)

	if err := ensureRunning(apiClient, cfg, name); err != nil {
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

		instanceID, err := apiClient.ResolveInstanceID(cfg.ApplicationID, name)
		if err != nil {
			lastErr = err
			continue
		}

		tunnelCreds, err := apiClient.GetSSHTunnel(instanceID)
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

func loginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Authenticate with Cloudflare via OAuth",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := auth.Login()
			if err != nil {
				return err
			}

			existing, _ := config.Load()
			if existing != nil {
				cfg.DONamespaceID = existing.DONamespaceID
				cfg.ApplicationID = existing.ApplicationID
				cfg.WorkerURL = existing.WorkerURL
			}

			if err := config.Save(cfg); err != nil {
				return err
			}

			if err := config.GenerateKeyPair(); err != nil {
				return fmt.Errorf("generating SSH keypair: %w", err)
			}

			pubKey, _ := config.ReadPublicKey()
			fmt.Printf("Logged in to account %s\n", cfg.AccountID)
			fmt.Printf("SSH public key: %s", pubKey)
			return nil
		},
	}
}

func setupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "One-time setup: deploy Worker, push image, create application",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			client := api.NewClient(cfg)

			exists, _ := client.WorkerExists()
			if !exists {
				fmt.Println("Deploying edgessh Worker (first time)...")
			} else {
				fmt.Println("Updating edgessh Worker...")
			}
			if err := client.UploadWorker(!exists); err != nil {
				return fmt.Errorf("uploading worker: %w", err)
			}

			fmt.Println("Enabling workers.dev subdomain...")
			if err := client.EnableWorkersDevSubdomain(); err != nil {
				return fmt.Errorf("enabling workers.dev subdomain: %w", err)
			}

			fmt.Println("Waiting for Durable Object namespace...")
			var nsID string
			for i := 0; i < 10; i++ {
				nsID, err = client.GetDONamespaceID()
				if err == nil {
					break
				}
				time.Sleep(2 * time.Second)
			}
			if err != nil {
				return fmt.Errorf("could not find DO namespace: %w", err)
			}
			cfg.DONamespaceID = nsID

			// Use a timestamp tag so each push creates a distinct image reference
			tag := fmt.Sprintf("v%d", time.Now().Unix())
			if err := client.PushImage(tag); err != nil {
				return err
			}

			imageRef := client.ImageRef(tag)

			pubKey, err := config.ReadPublicKey()
			if err != nil {
				return fmt.Errorf("reading public key (run 'edgessh login' first): %w", err)
			}

			app, err := client.GetApplicationByName("edgessh")
			if err != nil {
				// First time: create the application
				fmt.Println("Creating edgessh application...")
				app, err = client.CreateApplication(&api.CreateApplicationRequest{
					Name: "edgessh",
					Configuration: api.ApplicationConfig{
						Image:        imageRef,
						InstanceType: "standard-3",
						WranglerSSH: &api.WranglerSSHConfig{Enabled: true},
						AuthorizedKeys: []api.AuthorizedKey{
							{Name: "edgessh", PublicKey: strings.TrimSpace(pubKey)},
						},
					},
					MaxInstances:     10,
					Instances:        0,
					SchedulingPolicy: "default",
					DurableObjects:   &api.DOConfig{NamespaceID: nsID},
				})
				if err != nil {
					return fmt.Errorf("creating application: %w", err)
				}
			} else {
				// Update existing application to new image and rollout
				fmt.Printf("Updating application to %s...\n", imageRef)
				if err := client.ModifyApplication(app.ID, map[string]interface{}{
					"configuration": map[string]interface{}{
						"image": imageRef,
					},
				}); err != nil {
					return fmt.Errorf("updating application: %w", err)
				}

				fmt.Println("Rolling out new image...")
				if err := client.CreateRollout(app.ID, &api.CreateRolloutRequest{
					Description:    "edgessh setup " + tag,
					Strategy:       "rolling",
					Kind:           "full_auto",
					StepPercentage: 100,
					TargetConfiguration: map[string]interface{}{
						"image": imageRef,
					},
				}); err != nil {
					return fmt.Errorf("creating rollout: %w", err)
				}
			}

			cfg.ApplicationID = app.ID

			subdomain, err := client.GetWorkersSubdomain()
			if err != nil {
				return fmt.Errorf("getting workers subdomain: %w", err)
			}
			cfg.WorkerURL = fmt.Sprintf("https://edgessh.%s.workers.dev", subdomain)

			if err := config.Save(cfg); err != nil {
				return err
			}

			fmt.Printf("Setup complete!\n")
			fmt.Printf("  Application ID: %s\n", cfg.ApplicationID)
			fmt.Printf("  DO Namespace:   %s\n", cfg.DONamespaceID)
			fmt.Println("\nUse 'edgessh create NAME' to spin up a container.")
			return nil
		},
	}
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List container instances",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireSetup()
			if err != nil {
				return err
			}

			client := api.NewClient(cfg)
			resp, err := client.ListInstances(cfg.ApplicationID)
			if err != nil {
				return err
			}

			if len(resp.DurableObjects) == 0 && len(resp.Instances) == 0 {
				fmt.Println("No containers found. Use 'edgessh create NAME' to start one.")
				return nil
			}

			// Build instance status lookup by ID
			statusByID := make(map[string]string)
			for _, inst := range resp.Instances {
				status := "unknown"
				if inst.CurrentPlacement != nil && inst.CurrentPlacement.Status != nil {
					if s := inst.CurrentPlacement.Status.ContainerStatus; s != "" {
						status = s
					} else if s := inst.CurrentPlacement.Status.Health; s != "" {
						status = s
					}
				}
				statusByID[inst.ID] = status
			}

			fmt.Printf("%-20s %-12s %s\n", "NAME", "STATUS", "INSTANCE ID")
			for _, do := range resp.DurableObjects {
				status := "inactive"
				if do.DeploymentID != "" {
					if s, ok := statusByID[do.DeploymentID]; ok {
						status = s
					}
				}
				name := do.Name
				if name == "" {
					name = do.ID[:12]
				}
				fmt.Printf("%-20s %-12s %s\n", name, status, do.ID)
			}

			return nil
		},
	}
}

func createCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create INSTANCE_NAME",
		Short: "Create a new container instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cfg, err := requireSetup()
			if err != nil {
				return err
			}

			client := api.NewClient(cfg)

			fmt.Printf("Starting container %q...\n", name)
			if err := client.WakeContainer(cfg.WorkerURL, name); err != nil {
				return err
			}

			fmt.Println("Waiting for instance to start...")
			for i := 0; i < 60; i++ {
				instanceID, err := client.ResolveInstanceID(cfg.ApplicationID, name)
				if err == nil && instanceID != "" {
					fmt.Printf("Instance %s is running!\n", instanceID)
					fmt.Printf("Connect with: edgessh ssh %s\n", name)
					return nil
				}
				time.Sleep(5 * time.Second)
				fmt.Print(".")
			}

			fmt.Println("\nInstance not yet running. It may take a few minutes.")
			return nil
		},
	}
}

func sshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh INSTANCE_NAME [cmd [args...]]",
		Short: "SSH into a container instance",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireSetup()
			if err != nil {
				return err
			}

			client, err := dial(cfg, args[0])
			if err != nil {
				return err
			}
			defer client.Close()

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

			cfg, err := requireSetup()
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

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop INSTANCE_NAME",
		Short: "Stop a container instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireSetup()
			if err != nil {
				return err
			}

			client := api.NewClient(cfg)
			fmt.Printf("Stopping container %q...\n", args[0])
			return client.StopContainer(cfg.WorkerURL, args[0])
		},
	}
}

func exposeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "expose INSTANCE_NAME PORT",
		Short: "Expose a port from a container via SSH port forwarding",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, portStr := args[0], args[1]

			cfg, err := requireSetup()
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
