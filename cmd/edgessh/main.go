package main

import (
	"fmt"
	neturl "net/url"
	"os"
	"strings"

	"github.com/anthropics/edgessh/internal/config"
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

func requireWorkerAccess() (*config.Config, error) {
	cfg, err := requireSetup()
	if err != nil {
		return nil, err
	}
	if cfg.SessionToken == "" {
		return nil, fmt.Errorf("run 'edgessh auth login' first")
	}
	return cfg, nil
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
