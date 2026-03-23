package cfapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

type permissionGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *Client) listPermissionGroups() ([]permissionGroup, error) {
	var groups []permissionGroup
	url := fmt.Sprintf("%s/user/tokens/permission_groups", BaseAPIURL)
	if err := c.doGET(url, &groups); err != nil {
		return nil, err
	}
	return groups, nil
}

// findPermissionGroupIDs looks up permission group IDs by name.
func (c *Client) findPermissionGroupIDs(names ...string) (map[string]string, error) {
	groups, err := c.listPermissionGroups()
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool, len(names))
	for _, n := range names {
		wanted[n] = true
	}
	result := make(map[string]string, len(names))
	for _, g := range groups {
		if wanted[g.Name] {
			result[g.Name] = g.ID
		}
	}
	for _, n := range names {
		if result[n] == "" {
			return nil, fmt.Errorf("permission group %q not found", n)
		}
	}
	return result, nil
}

type tokenResponse struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

func (c *Client) createToken(name string, policies []map[string]interface{}) (*tokenResponse, error) {
	var resp tokenResponse
	url := fmt.Sprintf("%s/user/tokens", BaseAPIURL)
	if err := c.doPOST(url, map[string]interface{}{
		"name":     name,
		"policies": policies,
	}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CreateWorkersToken creates a scoped API token for Workers/Containers management.
func (c *Client) CreateWorkersToken() (string, error) {
	ids, err := c.findPermissionGroupIDs(
		"Workers Scripts Write",
		"Workers KV Storage Write",
		"Workers Routes Write",
		"Workers Tail Read",
		"Workers Containers Write",
		"Workers R2 Storage Write",
		"Account Settings Read",
	)
	if err != nil {
		return "", fmt.Errorf("looking up permission groups: %w", err)
	}

	accountResource := fmt.Sprintf("com.cloudflare.api.account.%s", c.cfg.AccountID)

	var permGroups []map[string]string
	for _, id := range ids {
		permGroups = append(permGroups, map[string]string{"id": id})
	}

	resp, err := c.createToken("edgessh-workers", []map[string]interface{}{
		{
			"effect":            "allow",
			"resources":         map[string]string{accountResource: "*"},
			"permission_groups": permGroups,
		},
	})
	if err != nil {
		return "", fmt.Errorf("creating workers token: %w", err)
	}
	return resp.Value, nil
}

// R2Credentials holds the S3-compatible credentials for R2 access.
type R2Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
}

// CreateR2Token creates a scoped API token for R2 S3 access to a specific bucket.
func (c *Client) CreateR2Token(bucketName string) (*R2Credentials, error) {
	ids, err := c.findPermissionGroupIDs("Workers R2 Storage Bucket Item Write")
	if err != nil {
		return nil, err
	}

	resource := fmt.Sprintf("com.cloudflare.edge.r2.bucket.%s_default_%s", c.cfg.AccountID, bucketName)

	resp, err := c.createToken(fmt.Sprintf("edgessh-r2-%s", bucketName), []map[string]interface{}{
		{
			"effect":    "allow",
			"resources": map[string]string{resource: "*"},
			"permission_groups": []map[string]string{
				{"id": ids["Workers R2 Storage Bucket Item Write"]},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("creating R2 token: %w", err)
	}

	// Access key ID = token ID, secret = SHA-256(token value)
	hash := sha256.Sum256([]byte(resp.Value))
	return &R2Credentials{
		AccessKeyID:     resp.ID,
		SecretAccessKey: hex.EncodeToString(hash[:]),
	}, nil
}

// CreateR2Bucket creates an R2 bucket via the Cloudflare API.
func (c *Client) CreateR2Bucket(name string) error {
	url := fmt.Sprintf("%s/accounts/%s/r2/buckets", BaseAPIURL, c.cfg.AccountID)
	return c.doPOST(url, map[string]string{"name": name}, nil)
}

// R2BucketExists checks if an R2 bucket exists.
func (c *Client) R2BucketExists(name string) (bool, error) {
	url := fmt.Sprintf("%s/accounts/%s/r2/buckets/%s", BaseAPIURL, c.cfg.AccountID, name)
	err := c.doGET(url, nil)
	if err != nil {
		return false, nil
	}
	return true, nil
}

// R2StoreURL returns the S3-compatible endpoint URL for the loophole store.
func (c *Client) R2StoreURL(bucketName string) string {
	return fmt.Sprintf("https://%s.r2.cloudflarestorage.com/%s", c.cfg.AccountID, bucketName)
}
