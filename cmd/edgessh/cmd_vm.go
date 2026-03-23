package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/edgessh/internal/cfapi"
	"github.com/anthropics/edgessh/internal/config"
	"github.com/anthropics/edgessh/internal/workerapi"
	"github.com/spf13/cobra"
)

func createCmd() *cobra.Command {
	var rootfs string
	var dockerRootfs string
	var size string
	cmd := &cobra.Command{
		Use:   "create VM_NAME",
		Short: "Create a Firecracker VM",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cfg, err := requireWorkerAccess()
			if err != nil {
				return err
			}

			// If --docker-rootfs, create a loophole volume from the given Docker image
			if dockerRootfs != "" {
				if rootfs == "" {
					rootfs = name
				}
				fmt.Printf("Creating rootfs volume %q from image %q...\n", rootfs, dockerRootfs)
				if err := createRootfsVolume(cfg, rootfs, dockerRootfs, size); err != nil {
					return fmt.Errorf("creating rootfs volume: %w", err)
				}
			}

			// Default: build the edgessh VM rootfs image and create a volume from it
			if rootfs == "" && dockerRootfs == "" {
				rootfs = name
				image, err := buildVMImage()
				if err != nil {
					return err
				}
				fmt.Printf("Creating rootfs volume %q from built image...\n", rootfs)
				if err := createRootfsVolume(cfg, rootfs, image, size); err != nil {
					return fmt.Errorf("creating rootfs volume: %w", err)
				}
			}

			wc := workerapi.NewClient(cfg.WorkerURL, cfg.SessionToken)
			pubKey, err := config.ReadPublicKey()
			if err != nil {
				return fmt.Errorf("reading SSH public key: %w", err)
			}

			fmt.Printf("Creating VM %q with rootfs %q...\n", name, rootfs)
			vm, err := wc.CreateVM(name, rootfs, strings.TrimSpace(pubKey))
			if err != nil {
				return fmt.Errorf("creating VM: %w", err)
			}

			fmt.Printf("VM %s ready on container %s! Connect with: edgessh ssh %s\n", name, vm.ContainerID, name)
			return nil
		},
	}
	cmd.Flags().StringVar(&rootfs, "rootfs", "", "Loophole volume name for VM rootfs (default: VM name)")
	cmd.Flags().StringVar(&dockerRootfs, "docker-rootfs", "", "Create rootfs from this Docker image instead of the default edgessh VM image")
	cmd.Flags().StringVar(&size, "size", "8GB", "Disk size for the rootfs volume (e.g. 8GB, 16GB)")
	return cmd
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List VMs",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireWorkerAccess()
			if err != nil {
				return err
			}

			wc := workerapi.NewClient(cfg.WorkerURL, cfg.SessionToken)
			vms, err := wc.ListVMs()
			if err != nil {
				return err
			}

			if len(vms) == 0 {
				fmt.Println("No VMs. Use 'edgessh create NAME --rootfs VOL' to create one.")
				return nil
			}

			fmt.Printf("%-20s %-10s %-12s %-10s\n", "VM", "STATUS", "CONTAINER", "ROOTFS")
			for _, vm := range vms {
				status := "stopped"
				cid := "-"
				if vm.ContainerID != "" {
					status = "running"
					cid = vm.ContainerID
				}
				fmt.Printf("%-20s %-10s %-12s %-10s\n", vm.Name, status, cid, vm.Rootfs)
			}
			return nil
		},
	}
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop VM_NAME",
		Short: "Stop a VM (preserves data, can be restarted with ssh)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cfg, err := requireWorkerAccess()
			if err != nil {
				return err
			}

			wc := workerapi.NewClient(cfg.WorkerURL, cfg.SessionToken)
			if err := wc.StopVM(name); err != nil {
				return err
			}

			fmt.Printf("VM %q stopped (data preserved, use 'edgessh ssh %s' to restart)\n", name, name)
			return nil
		},
	}
}

func deleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete VM_NAME",
		Short: "Permanently delete a VM and its data",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cfg, err := requireWorkerAccess()
			if err != nil {
				return err
			}

			wc := workerapi.NewClient(cfg.WorkerURL, cfg.SessionToken)
			if err := wc.DeleteVM(name); err != nil {
				return err
			}

			fmt.Printf("VM %q deleted\n", name)
			return nil
		},
	}
}

func checkpointCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "checkpoint VM_NAME",
		Short: "Checkpoint a running VM (pause, snapshot rootfs, resume)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cfg, err := requireWorkerAccess()
			if err != nil {
				return err
			}

			wc := workerapi.NewClient(cfg.WorkerURL, cfg.SessionToken)

			fmt.Printf("Checkpointing VM %q (pause → snapshot → resume)...\n", name)
			cpID, err := wc.CheckpointVM(name)
			if err != nil {
				return err
			}

			fmt.Printf("Checkpoint created: %s\n", cpID)
			return nil
		},
	}
}

func resetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Delete the application and all containers/VMs (destructive)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireSetup()
			if err != nil {
				return err
			}

			fmt.Printf("This will DELETE the edgessh application, all containers, and all VMs.\n")
			fmt.Printf("Application ID: %s\n\n", cfg.ApplicationID)
			fmt.Print("Type 'yes' to confirm: ")
			var confirm string
			fmt.Scanln(&confirm)
			if confirm != "yes" {
				fmt.Println("Aborted.")
				return nil
			}

			apiClient := cfapi.NewClient(cfg)
			fmt.Println("Deleting application...")
			if err := apiClient.DeleteApplication(cfg.ApplicationID); err != nil {
				return fmt.Errorf("deleting application: %w", err)
			}

			fmt.Println("Deleting Worker...")
			if err := apiClient.DeleteWorker(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not delete worker: %v\n", err)
			}

			if cfg.DONamespaceID != "" {
				fmt.Println("Deleting DO namespace...")
				if err := apiClient.DeleteDONamespace(cfg.DONamespaceID); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not delete DO namespace: %v\n", err)
				}
			}

			cfg.ApplicationID = ""
			cfg.DONamespaceID = ""
			cfg.WorkerURL = ""
			cfg.WorkerAuthSecret = ""
			cfg.SessionToken = ""
			cfg.SessionSubject = ""
			cfg.SessionName = ""
			cfg.APIToken = ""
			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}

			fmt.Println("Application and Worker deleted. Run 'edgessh setup' to recreate.")
			return nil
		},
	}
}
