package cfapi

import "fmt"

const CloudflareRegistry = "registry.cloudflare.com"

type RegistryCredentials struct {
	AccountID    string `json:"account_id"`
	RegistryHost string `json:"registry_host"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

type RegistryCredentialsRequest struct {
	ExpirationMinutes int      `json:"expiration_minutes"`
	Permissions       []string `json:"permissions"`
}

func (c *Client) GenerateRegistryCredentials(push, pull bool) (*RegistryCredentials, error) {
	var permissions []string
	if push {
		permissions = append(permissions, "push")
	}
	if pull {
		permissions = append(permissions, "pull")
	}

	var creds RegistryCredentials
	err := c.doPOST(fmt.Sprintf("%s/registries/%s/credentials", c.containersURL(), CloudflareRegistry),
		RegistryCredentialsRequest{ExpirationMinutes: 15, Permissions: permissions},
		&creds)
	if err != nil {
		return nil, fmt.Errorf("generating registry credentials: %w", err)
	}
	if creds.Password == "" {
		return nil, fmt.Errorf("empty credentials returned — check that containers are enabled for your account")
	}
	return &creds, nil
}

func (c *Client) ImageRef(tag string) string {
	return fmt.Sprintf("%s/%s/edgessh:%s", CloudflareRegistry, c.cfg.AccountID, tag)
}
