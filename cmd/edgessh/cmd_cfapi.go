package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/anthropics/edgessh/internal/cfapi"
	"github.com/anthropics/edgessh/internal/config"
	"github.com/spf13/cobra"
)

func cfapiCmd() *cobra.Command {
	var token string
	cmd := &cobra.Command{
		Use:   "cfapi METHOD PATH",
		Short: "Make a raw Cloudflare API request",
		Long: `Make a raw Cloudflare API request.

PATH is relative to the account URL, e.g.:
  edgessh cfapi GET /workers/scripts
  edgessh cfapi GET /containers/applications

Uses the scoped Workers token by default. Use --token master for the master token.

API docs: https://developers.cloudflare.com/api/`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			method := strings.ToUpper(args[0])
			path := args[1]

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			bearerToken := cfg.BearerToken()
			if token == "master" {
				bearerToken = cfg.MasterToken
			}

			url := fmt.Sprintf("%s/accounts/%s%s", cfapi.BaseAPIURL, cfg.AccountID, path)

			req, err := http.NewRequest(method, url, nil)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+bearerToken)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			fmt.Fprintf(os.Stderr, "%s %s → %d\n", method, path, resp.StatusCode)
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			var pretty json.RawMessage
			if json.Unmarshal(body, &pretty) == nil {
				formatted, _ := json.MarshalIndent(pretty, "", "  ")
				os.Stdout.Write(formatted)
				fmt.Println()
			} else {
				os.Stdout.Write(body)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "Token to use: 'master' for master token (default: scoped workers token)")
	return cmd
}
