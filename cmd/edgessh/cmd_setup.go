package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/edgessh/internal/cfapi"
	"github.com/anthropics/edgessh/internal/config"
	"github.com/spf13/cobra"
)

func setupCmd() *cobra.Command {
	var token string
	var only string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "One-time setup: authenticate, deploy Worker, push image, create application",
		Long: `One-time setup for edgessh.

Pass --token with a Cloudflare API token that has "User > API Tokens > Edit" permission.
Create one at https://dash.cloudflare.com/profile/api-tokens

This master token is used to mint scoped tokens for Workers, Containers, and R2.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _ := config.Load()
			if cfg == nil {
				cfg = &config.Config{}
			}

			if token != "" {
				cfg.MasterToken = token
			}

			if cfg.MasterToken == "" {
				return fmt.Errorf("run 'edgessh setup --token <API_TOKEN>'\n\nCreate a token at https://dash.cloudflare.com/profile/api-tokens\nUse the \"Create Additional Tokens\" template (User > API Tokens > Edit)")
			}
			if err := config.EnsureWorkerAuthSecret(cfg); err != nil {
				return fmt.Errorf("creating worker auth secret: %w", err)
			}

			// Use master token for initial API calls
			client := cfapi.NewClient(cfg)

			if cfg.AccountID == "" {
				fmt.Println("Verifying master token...")
				accountID, err := client.GetAccountID()
				if err != nil {
					return fmt.Errorf("verifying token: %w", err)
				}
				cfg.AccountID = accountID
				fmt.Printf("Account: %s\n", accountID)
			}

			// Mint a scoped workers token if we don't have one
			if cfg.APIToken == "" {
				fmt.Println("Creating scoped Workers/Containers token...")
				workerToken, err := client.CreateWorkersToken()
				if err != nil {
					return fmt.Errorf("creating workers token: %w", err)
				}
				cfg.APIToken = workerToken
				// Switch client to use the scoped token
				client = cfapi.NewClient(cfg)
			}

			if err := config.GenerateKeyPair(); err != nil {
				return fmt.Errorf("generating SSH keypair: %w", err)
			}

			// --- Deploy Worker ---
			exists, _ := client.WorkerExists()
			if !exists {
				fmt.Println("Deploying edgessh Worker (first time)...")
			} else {
				fmt.Println("Updating edgessh Worker...")
			}
			if err := client.UploadWorker(!exists, nil); err != nil {
				return fmt.Errorf("uploading worker: %w", err)
			}
			if err := putWorkerSecrets(client, cfg); err != nil {
				return err
			}

			fmt.Println("Enabling workers.dev subdomain...")
			if err := client.EnableWorkersDevSubdomain(); err != nil {
				return fmt.Errorf("enabling workers.dev subdomain: %w", err)
			}

			fmt.Println("Waiting for Durable Object namespace...")
			var nsID string
			var err error
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

			if only == "worker" {
				subdomain, err := client.GetWorkersSubdomain()
				if err != nil {
					return fmt.Errorf("getting workers subdomain: %w", err)
				}
				cfg.WorkerURL = fmt.Sprintf("https://edgessh.%s.workers.dev", subdomain)
				if err := config.Save(cfg); err != nil {
					return err
				}
				fmt.Println("Worker deployed.")
				return nil
			}

			// --- Push image + rollout ---
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
				fmt.Println("Creating edgessh application...")
				app, err = client.CreateApplication(&cfapi.CreateApplicationRequest{
					Name: "edgessh",
					Configuration: cfapi.ApplicationConfig{
						Image:       imageRef,
						VCPU:        4,
						Memory:      "12GiB",
						Disk:        &cfapi.ApplicationDisk{Size: "20GB"},
						WranglerSSH: &cfapi.WranglerSSHConfig{Enabled: true},
						AuthorizedKeys: []cfapi.AuthorizedKey{
							{Name: "edgessh", PublicKey: strings.TrimSpace(pubKey)},
						},
					},
					MaxInstances:     10,
					Instances:        0,
					SchedulingPolicy: "default",
					DurableObjects:   &cfapi.DOConfig{NamespaceID: nsID},
				})
				if err != nil {
					return fmt.Errorf("creating application: %w", err)
				}
			} else {
				fmt.Printf("Updating application to %s...\n", imageRef)
				if err := client.ModifyApplication(app.ID, map[string]interface{}{
					"configuration": map[string]interface{}{
						"image":  imageRef,
						"vcpu":   4,
						"memory": "12GiB",
						"disk": map[string]interface{}{
							"size": "20GB",
						},
					},
				}); err != nil {
					return fmt.Errorf("updating application: %w", err)
				}

				fmt.Println("Rolling out new image...")
				if err := client.CreateRollout(app.ID, &cfapi.CreateRolloutRequest{
					Description:    "edgessh setup " + tag,
					Strategy:       "rolling",
					Kind:           "full_auto",
					StepPercentage: 100,
					TargetConfiguration: map[string]interface{}{
						"image":  imageRef,
						"vcpu":   4,
						"memory": "12GiB",
						"disk": map[string]interface{}{
							"size": "20GB",
						},
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

			// --- R2 + loophole setup ---
			// Use a fresh bucket name because older stores were formatted with a
			// different loophole page size and are not compatible with the current
			// linux runtime in the container.
			const loopholeBucket = "edgessh-loophole-v3"
			desiredStoreURL := client.R2StoreURL(loopholeBucket)
			if cfg.R2AccessKeyID == "" || cfg.LoopholeStoreURL != desiredStoreURL {
				r2Exists, _ := client.R2BucketExists(loopholeBucket)
				if !r2Exists {
					fmt.Println("Creating R2 bucket for loophole volumes...")
					if err := client.CreateR2Bucket(loopholeBucket); err != nil {
						return fmt.Errorf("creating R2 bucket: %w", err)
					}
				}

				masterCfg := &config.Config{MasterToken: cfg.MasterToken, AccountID: cfg.AccountID}
				masterClient := cfapi.NewClient(masterCfg)

				fmt.Println("Creating R2 API token for loophole...")
				creds, err := masterClient.CreateR2Token(loopholeBucket)
				if err != nil {
					return fmt.Errorf("creating R2 token: %w", err)
				}
				cfg.R2AccessKeyID = creds.AccessKeyID
				cfg.R2SecretAccessKey = creds.SecretAccessKey
				cfg.LoopholeStoreURL = desiredStoreURL

				fmt.Println("Updating Worker secrets with R2 credentials...")
				if err := putWorkerSecrets(client, cfg); err != nil {
					return err
				}
			}

			fmt.Println("Formatting loophole store...")
			if err := runLoophole(cfg, "format", cfg.LoopholeStoreURL); err != nil {
				return fmt.Errorf("formatting loophole store: %w", err)
			}

			if err := config.Save(cfg); err != nil {
				return err
			}

			fmt.Printf("Setup complete!\n")
			fmt.Printf("  Application ID: %s\n", cfg.ApplicationID)
			fmt.Printf("  DO Namespace:   %s\n", cfg.DONamespaceID)
			fmt.Printf("  Loophole Store: %s\n", cfg.LoopholeStoreURL)
			fmt.Println("\nUse 'edgessh create NAME --rootfs VOLUME' to spin up a VM.")
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "Cloudflare API token")
	cmd.Flags().StringVar(&only, "only", "", "Deploy only a specific component: worker")
	return cmd
}

func putWorkerSecrets(client *cfapi.Client, cfg *config.Config) error {
	if err := client.PutWorkerSecret("EDGESSH_AUTH_SECRET", cfg.WorkerAuthSecret); err != nil {
		return fmt.Errorf("setting worker auth secret: %w", err)
	}
	if cfg.LoopholeStoreURL != "" {
		if err := client.PutWorkerSecret("LOOPHOLE_STORE_URL", cfg.LoopholeStoreURL); err != nil {
			return fmt.Errorf("setting worker loophole store URL: %w", err)
		}
	}
	if cfg.R2AccessKeyID != "" {
		if err := client.PutWorkerSecret("AWS_ACCESS_KEY_ID", cfg.R2AccessKeyID); err != nil {
			return fmt.Errorf("setting worker R2 access key ID: %w", err)
		}
	}
	if cfg.R2SecretAccessKey != "" {
		if err := client.PutWorkerSecret("AWS_SECRET_ACCESS_KEY", cfg.R2SecretAccessKey); err != nil {
			return fmt.Errorf("setting worker R2 secret access key: %w", err)
		}
	}
	return nil
}
