package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/anthropics/edgessh/internal/config"
	"github.com/anthropics/edgessh/internal/workerapi"
	"github.com/spf13/cobra"
)

const vumelaBaseURL = "https://vumela.dev"

type deviceStartResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Error                   string `json:"error"`
}

type devicePollResponse struct {
	Status string `json:"status"`
	Token  string `json:"token"`
	Error  string `json:"error"`
}

func authCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate edgessh against the Worker via Vumela",
	}
	cmd.AddCommand(authLoginCmd())
	cmd.AddCommand(authLogoutCmd())
	cmd.AddCommand(authStatusCmd())
	return cmd
}

func authLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Authenticate with Vumela and store an edgessh session token",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireSetup()
			if err != nil {
				return err
			}

			startResp, err := startExternalDeviceFlow(cfg.WorkerURL)
			if err != nil {
				return err
			}

			fmt.Printf("Open this URL to approve edgessh:\n\n%s\n\n", startResp.VerificationURIComplete)
			fmt.Printf("If prompted, enter code: %s\n", startResp.UserCode)
			if err := openBrowser(startResp.VerificationURIComplete); err != nil {
				fmt.Fprintf(os.Stderr, "Could not open browser automatically: %v\n", err)
			}

			vumelaToken, err := pollExternalDeviceFlow(startResp)
			if err != nil {
				return err
			}

			wc := workerapi.NewClient(cfg.WorkerURL, "")
			session, err := wc.ExchangeVumelaToken(vumelaToken)
			if err != nil {
				return fmt.Errorf("exchanging Vumela token with worker: %w", err)
			}

			cfg.SessionToken = session.SessionToken
			cfg.SessionSubject = session.Subject
			cfg.SessionName = session.Name
			if err := config.Save(cfg); err != nil {
				return err
			}

			fmt.Printf("Authenticated as %s (%s)\n", session.Name, session.Subject)
			return nil
		},
	}
}

func authLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the stored edgessh session token",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireSetup()
			if err != nil {
				return err
			}
			cfg.SessionToken = ""
			cfg.SessionSubject = ""
			cfg.SessionName = ""
			return config.Save(cfg)
		},
	}
}

func authStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the current edgessh auth status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireSetup()
			if err != nil {
				return err
			}
			if cfg.SessionToken == "" {
				fmt.Println("Not authenticated. Run 'edgessh auth login'.")
				return nil
			}

			wc := workerapi.NewClient(cfg.WorkerURL, cfg.SessionToken)
			me, err := wc.Me()
			if err != nil {
				return fmt.Errorf("session present but validation failed: %w", err)
			}

			fmt.Printf("Authenticated as %s (%s)\n", me.Name, me.Subject)
			return nil
		},
	}
}

func startExternalDeviceFlow(workerURL string) (*deviceStartResponse, error) {
	body := map[string]string{
		"redirect_url": workerURL,
		"app_name":     "edgessh",
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, vumelaBaseURL+"/api/auth/external-device/start", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result deviceStartResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Vumela start failed: %s", result.Error)
	}
	return &result, nil
}

func pollExternalDeviceFlow(start *deviceStartResponse) (string, error) {
	deadline := time.Now().Add(time.Duration(start.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		data, err := json.Marshal(map[string]string{"device_code": start.DeviceCode})
		if err != nil {
			return "", err
		}
		req, err := http.NewRequest(http.MethodPost, vumelaBaseURL+"/api/auth/external-device/poll", bytes.NewReader(data))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		var poll devicePollResponse
		err = json.NewDecoder(resp.Body).Decode(&poll)
		resp.Body.Close()
		if err != nil {
			return "", err
		}

		switch poll.Status {
		case "pending":
			time.Sleep(2 * time.Second)
			continue
		case "denied":
			return "", fmt.Errorf("authentication denied")
		case "granted":
			if poll.Token == "" {
				return "", fmt.Errorf("device flow completed without token")
			}
			return poll.Token, nil
		default:
			if poll.Error != "" {
				return "", fmt.Errorf("device flow failed: %s", poll.Error)
			}
			return "", fmt.Errorf("unexpected poll status %q", poll.Status)
		}
	}
	return "", fmt.Errorf("authentication timed out")
}

func openBrowser(url string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "linux":
		command = exec.Command("xdg-open", url)
	default:
		return fmt.Errorf("unsupported OS %s", runtime.GOOS)
	}
	return command.Start()
}
