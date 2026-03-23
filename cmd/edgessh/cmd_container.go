package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/edgessh/internal/sshclient"
	"github.com/anthropics/edgessh/internal/workerapi"
	"github.com/spf13/cobra"
)

func containerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "container",
		Short: "Manage containers (VM hosts)",
	}
	cmd.AddCommand(containerListCmd())
	cmd.AddCommand(containerSSHCmd())
	cmd.AddCommand(containerStopCmd())
	return cmd
}

func containerListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List containers",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireWorkerAccess()
			if err != nil {
				return err
			}

			wc := workerapi.NewClient(cfg.WorkerURL, cfg.SessionToken)
			containers, err := wc.ListContainers()
			if err != nil {
				return err
			}

			if len(containers) == 0 {
				fmt.Println("No containers.")
				return nil
			}

			fmt.Printf("%-10s %-10s %s\n", "ID", "VMS", "MAX")
			for _, c := range containers {
				fmt.Printf("%-10s %-10d %d\n", c.ID, c.VMCount, c.MaxVMs)
			}
			return nil
		},
	}
}

func containerSSHCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh CONTAINER_ID [cmd [args...]]",
		Short: "SSH into a container directly",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireWorkerAccess()
			if err != nil {
				return err
			}

			wc := workerapi.NewClient(cfg.WorkerURL, cfg.SessionToken)
			containers, err := wc.ListContainers()
			if err != nil {
				return err
			}

			containerID := args[0]
			var doName string
			for _, c := range containers {
				if c.ID == containerID {
					doName = c.DOName
					break
				}
			}
			if doName == "" {
				return fmt.Errorf("container %q not found", containerID)
			}

			client, err := dial(cfg, doName)
			if err != nil {
				return err
			}
			defer client.Close()

			keepaliveCtx, cancelKeepalive := context.WithCancel(context.Background())
			defer cancelKeepalive()
			startContainerKeepalive(keepaliveCtx, cfg.WorkerURL, doName, cfg.SessionToken)

			if len(args) > 1 {
				return sshclient.Exec(client, strings.Join(args[1:], " "))
			}
			return sshclient.Shell(client)
		},
	}
}

func containerStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop CONTAINER_ID",
		Short: "Stop a container (stops all its VMs)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireWorkerAccess()
			if err != nil {
				return err
			}

			wc := workerapi.NewClient(cfg.WorkerURL, cfg.SessionToken)
			containers, err := wc.ListContainers()
			if err != nil {
				return err
			}

			containerID := args[0]
			var doName string
			for _, c := range containers {
				if c.ID == containerID {
					doName = c.DOName
					break
				}
			}
			if doName == "" {
				return fmt.Errorf("container %q not found", containerID)
			}

			fmt.Printf("Stopping container %s...\n", containerID)
			return wc.StopContainer(doName)
		},
	}
}
