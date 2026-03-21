package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/anthropics/edgessh/internal/api"
	"github.com/anthropics/edgessh/internal/auth"
	"github.com/anthropics/edgessh/internal/config"
	"github.com/anthropics/edgessh/internal/tunnel"
	"github.com/spf13/cobra"
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
	root.AddCommand(sshCmd())
	root.AddCommand(stopCmd())
	root.AddCommand(exposeCmd())
	root.AddCommand(scpCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
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

			// Preserve existing setup state if re-logging in
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

			// 1. Deploy Worker
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

			// 2. Get DO namespace ID
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

			// 3. Push image to Cloudflare registry
			tag := "v1"
			if err := client.PushImage(tag); err != nil {
				return err
			}

			// 4. Create the single application (or update if exists)
			pubKey, err := config.ReadPublicKey()
			if err != nil {
				return fmt.Errorf("reading public key (run 'edgessh login' first): %w", err)
			}

			imageRef := client.ImageRef(tag)

			app, err := client.GetApplicationByName("edgessh")
			if err != nil {
				// Doesn't exist yet, create it
				fmt.Println("Creating edgessh application...")
				app, err = client.CreateApplication(&api.CreateApplicationRequest{
					Name: "edgessh",
					Configuration: api.ApplicationConfig{
						Image:        imageRef,
						InstanceType: "dev",
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
				fmt.Println("Application already exists, skipping creation.")
			}

			cfg.ApplicationID = app.ID

			// 5. Determine worker URL
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

func createCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create INSTANCE_NAME",
		Short: "Create a new container instance (DO instance inside the edgessh Worker)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.ApplicationID == "" || cfg.WorkerURL == "" {
				return fmt.Errorf("run 'edgessh setup' first")
			}

			client := api.NewClient(cfg)

			// Wake the DO instance by hitting the Worker
			fmt.Printf("Starting container %q...\n", name)
			if err := client.WakeContainer(cfg.WorkerURL, name); err != nil {
				return err
			}

			// Poll until we can resolve the instance ID
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

func resolveInstance(client *api.Client, cfg *config.Config, name string) (string, error) {
	return client.ResolveInstanceID(cfg.ApplicationID, name)
}

func sshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh INSTANCE_NAME [cmd [args...]]",
		Short: "SSH into a container instance",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			remoteCmd := args[1:]

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.ApplicationID == "" {
				return fmt.Errorf("run 'edgessh setup' first")
			}

			client := api.NewClient(cfg)

			instanceID, err := resolveInstance(client, cfg, name)
			if err != nil {
				return err
			}

			sshTunnel, err := client.GetSSHTunnel(instanceID)
			if err != nil {
				return err
			}

			proxy := tunnel.NewProxy(sshTunnel.URL, sshTunnel.Token)
			port, err := proxy.Start()
			if err != nil {
				return err
			}
			defer proxy.Close()

			sshArgs := []string{
				"cloudchamber@127.0.0.1",
				"-p", fmt.Sprintf("%d", port),
				"-i", config.PrivateKeyPath(),
				"-t", // force PTY allocation
				"-o", "ControlMaster=no",
				"-o", "ControlPersist=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "StrictHostKeyChecking=no",
				"-o", "LogLevel=ERROR",
			}
			if len(remoteCmd) > 0 {
				sshArgs = append(sshArgs, "--")
				sshArgs = append(sshArgs, remoteCmd...)
			} else {
				// Interactive: request bash login shell
				sshArgs = append(sshArgs, "--", "bash", "-l")
			}

			sshExec := exec.Command("ssh", sshArgs...)
			sshExec.Stdin = os.Stdin
			sshExec.Stdout = os.Stdout
			sshExec.Stderr = os.Stderr

			if err := sshExec.Run(); err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 255 {
					return fmt.Errorf("SSH connection failed. Is the container running?\nSSH does not automatically wake a container or count as activity")
				}
				return err
			}
			return nil
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

			var name string
			if parts := strings.SplitN(src, ":", 2); len(parts) == 2 && !strings.HasPrefix(src, "/") {
				name = parts[0]
			} else if parts := strings.SplitN(dst, ":", 2); len(parts) == 2 && !strings.HasPrefix(dst, "/") {
				name = parts[0]
			} else {
				return fmt.Errorf("one of SRC or DST must be INSTANCE_NAME:/path")
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			client := api.NewClient(cfg)
			instanceID, err := resolveInstance(client, cfg, name)
			if err != nil {
				return err
			}

			sshTunnel, err := client.GetSSHTunnel(instanceID)
			if err != nil {
				return err
			}

			proxy := tunnel.NewProxy(sshTunnel.URL, sshTunnel.Token)
			port, err := proxy.Start()
			if err != nil {
				return err
			}
			defer proxy.Close()

			rewrite := func(s string) string {
				if parts := strings.SplitN(s, ":", 2); len(parts) == 2 && parts[0] == name {
					return fmt.Sprintf("cloudchamber@127.0.0.1:%s", parts[1])
				}
				return s
			}

			scpArgs := []string{
				"-P", fmt.Sprintf("%d", port),
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "StrictHostKeyChecking=no",
				"-o", "LogLevel=ERROR",
			}
			if recursive {
				scpArgs = append(scpArgs, "-r")
			}
			scpArgs = append(scpArgs, rewrite(src), rewrite(dst))

			scpExec := exec.Command("scp", scpArgs...)
			scpExec.Stdin = os.Stdin
			scpExec.Stdout = os.Stdout
			scpExec.Stderr = os.Stderr
			return scpExec.Run()
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
			name := args[0]
			_ = name
			// TODO: send stop signal to the DO instance via the Worker
			fmt.Println("Not yet implemented — stop the container via the Cloudflare dashboard")
			return nil
		},
	}
}

func exposeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "expose INSTANCE_NAME PORT",
		Short: "Expose a port from a container via SSH port forwarding",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			port := args[1]

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			client := api.NewClient(cfg)
			instanceID, err := resolveInstance(client, cfg, name)
			if err != nil {
				return err
			}

			sshTunnel, err := client.GetSSHTunnel(instanceID)
			if err != nil {
				return err
			}

			proxy := tunnel.NewProxy(sshTunnel.URL, sshTunnel.Token)
			proxyPort, err := proxy.Start()
			if err != nil {
				return err
			}
			defer proxy.Close()

			fmt.Printf("Forwarding localhost:%s → %s:%s\n", port, name, port)
			fmt.Println("Press Ctrl+C to stop")

			sshExec := exec.Command("ssh",
				"cloudchamber@127.0.0.1",
				"-p", fmt.Sprintf("%d", proxyPort),
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "StrictHostKeyChecking=no",
				"-o", "LogLevel=ERROR",
				"-N",
				"-L", fmt.Sprintf("%s:localhost:%s", port, port),
			)
			sshExec.Stdin = os.Stdin
			sshExec.Stdout = os.Stdout
			sshExec.Stderr = os.Stderr
			return sshExec.Run()
		},
	}
}
