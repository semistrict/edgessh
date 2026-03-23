package main

import (
	"fmt"
	neturl "net/url"
	"os"
	"strings"

	"github.com/anthropics/edgessh/internal/config"
	"github.com/anthropics/edgessh/internal/workerapi"
	"github.com/spf13/cobra"
)

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func main() {
	root := &cobra.Command{
		Use:           "edgessh",
		Short:         "SSH into Cloudflare Containers",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(setupCmd())
	root.AddCommand(authCmd())
	root.AddCommand(createCmd())
	root.AddCommand(listCmd())
	root.AddCommand(sshCmd())
	root.AddCommand(stopCmd())
	root.AddCommand(deleteCmd())
	root.AddCommand(containerCmd())
	root.AddCommand(checkpointCmd())
	root.AddCommand(resetCmd())
	root.AddCommand(exposeCmd())
	root.AddCommand(scpCmd())
	root.AddCommand(cfapiCmd())
	root.AddCommand(loopholeCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func requireSetup() (*config.Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if cfg.ApplicationID == "" || cfg.WorkerURL == "" {
		return nil, fmt.Errorf("run 'edgessh setup' first")
	}
	return cfg, nil
}

func requireWorkerSetup() (*config.Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if cfg.WorkerURL == "" {
		return nil, fmt.Errorf("run 'edgessh auth login --url <WORKER_URL>' or 'edgessh setup' first")
	}
	return cfg, nil
}

func requireWorkerAccess() (*config.Config, error) {
	cfg, err := requireWorkerSetup()
	if err != nil {
		return nil, err
	}
	if cfg.SessionToken == "" {
		if cfg.WorkerAuthSecret == "" {
			return nil, fmt.Errorf("run 'EDGESSH_SHARED_SECRET=<SECRET> edgessh auth login --url <WORKER_URL>' or 'edgessh auth login --url <WORKER_URL> --vumela' first")
		}
		if err := ensureWorkerSession(cfg); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func requireCloudflareAccess() (*config.Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if cfg.AccountID == "" || cfg.ApplicationID == "" || cfg.BearerToken() == "" {
		return nil, fmt.Errorf("run 'edgessh setup --token <CLOUDFLARE_API_TOKEN>' first")
	}
	return cfg, nil
}

func requireTunnelAccess() (*config.Config, error) {
	cfg, err := requireWorkerAccess()
	if err != nil {
		return nil, err
	}
	if cfg.AccountID == "" || cfg.ApplicationID == "" || cfg.BearerToken() == "" {
		return nil, fmt.Errorf("this command still requires Cloudflare API access; run 'edgessh setup --token <CLOUDFLARE_API_TOKEN>' first")
	}
	return cfg, nil
}

func ensureWorkerSession(cfg *config.Config) error {
	if cfg.WorkerURL == "" {
		return fmt.Errorf("missing worker URL; run 'edgessh auth login --url <WORKER_URL>' or 'edgessh setup' first")
	}
	if cfg.SessionToken != "" {
		return nil
	}
	if cfg.WorkerAuthSecret == "" {
		return fmt.Errorf("missing worker shared secret")
	}

	wc := workerapi.NewClient(cfg.WorkerURL, "")
	session, err := wc.ExchangeSharedSecret(cfg.WorkerAuthSecret)
	if err != nil {
		return fmt.Errorf("authenticating with worker shared secret: %w", err)
	}

	cfg.SessionToken = session.SessionToken
	cfg.SessionSubject = session.Subject
	cfg.SessionName = session.Name
	return config.Save(cfg)
}

func workerWebSocketURL(workerURL, path string, params map[string]string) (string, error) {
	u, err := neturl.Parse(workerURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported worker URL scheme %q", u.Scheme)
	}
	u.Path = path
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
