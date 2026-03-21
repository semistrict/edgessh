package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"

	"github.com/anthropics/edgessh/internal/auth"

	edgesshEmbed "github.com/anthropics/edgessh/embed"
)

// workerMetadata builds the metadata JSON dynamically.
// On first deploy, includes migrations with new_sqlite_classes.
// On subsequent deploys, omits migrations to avoid the "already exists" error.
func workerMetadata(firstTime bool) ([]byte, error) {
	meta := map[string]any{
		"main_module":        "worker.mjs",
		"compatibility_date": "2026-03-20",
		"bindings": []map[string]string{
			{
				"type":       "durable_object_namespace",
				"name":       "EDGESSH",
				"class_name": "EdgeSSH",
			},
		},
		"containers": []map[string]string{
			{"class_name": "EdgeSSH"},
		},
	}

	if firstTime {
		meta["migrations"] = map[string]any{
			"tag":                "v1",
			"new_sqlite_classes": []string{"EdgeSSH"},
		}
	}

	return json.Marshal(meta)
}

// UploadWorker uploads the edgessh Worker script via the Workers API.
// Uses multipart/form-data exactly as wrangler does:
// - "metadata" part: application/json
// - module part: application/javascript+module for ESM
func (c *Client) UploadWorker(firstTime bool) error {
	metadataJSON, err := workerMetadata(firstTime)
	if err != nil {
		return fmt.Errorf("building metadata: %w", err)
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Part 1: metadata (application/json)
	metadataHeader := make(textproto.MIMEHeader)
	metadataHeader.Set("Content-Disposition", `form-data; name="metadata"; filename="metadata.json"`)
	metadataHeader.Set("Content-Type", "application/json")
	metadataPart, err := writer.CreatePart(metadataHeader)
	if err != nil {
		return fmt.Errorf("creating metadata part: %w", err)
	}
	metadataPart.Write(metadataJSON)

	// Part 2: worker module — Content-Type MUST be application/javascript+module for ESM
	moduleHeader := make(textproto.MIMEHeader)
	moduleHeader.Set("Content-Disposition", `form-data; name="worker.mjs"; filename="worker.mjs"`)
	moduleHeader.Set("Content-Type", "application/javascript+module")
	modulePart, err := writer.CreatePart(moduleHeader)
	if err != nil {
		return fmt.Errorf("creating module part: %w", err)
	}
	modulePart.Write(edgesshEmbed.WorkerJS)

	writer.Close()

	return c.doPUT(
		fmt.Sprintf("%s/scripts/edgessh", c.workersURL()),
		&buf, writer.FormDataContentType(), nil,
	)
}

type DONamespace struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Class  string `json:"class"`
	Script string `json:"script"`
}

// GetDONamespaceID fetches the Durable Object namespace ID for the EdgeSSH class.
func (c *Client) GetDONamespaceID() (string, error) {
	var namespaces []DONamespace
	url := fmt.Sprintf("%s/accounts/%s/workers/durable_objects/namespaces?per_page=1000", BaseAPIURL, c.cfg.AccountID)
	if err := c.doGET(url, &namespaces); err != nil {
		return "", err
	}

	for _, ns := range namespaces {
		if ns.Class == "EdgeSSH" && ns.Script == "edgessh" {
			return ns.ID, nil
		}
	}

	return "", fmt.Errorf("EdgeSSH Durable Object namespace not found — is the worker deployed?")
}

// EnableWorkersDevSubdomain enables the workers.dev route for the edgessh script.
func (c *Client) EnableWorkersDevSubdomain() error {
	url := fmt.Sprintf("%s/scripts/edgessh/subdomain", c.workersURL())
	return c.doPOST(url, map[string]bool{"enabled": true}, nil)
}

// GetWorkersSubdomain returns the account's workers.dev subdomain.
func (c *Client) GetWorkersSubdomain() (string, error) {
	var resp struct {
		Subdomain string `json:"subdomain"`
	}
	if err := c.doGET(fmt.Sprintf("%s/accounts/%s/workers/subdomain", BaseAPIURL, c.cfg.AccountID), &resp); err != nil {
		return "", err
	}
	return resp.Subdomain, nil
}

// WorkerExists checks if the edgessh worker script already exists.
func (c *Client) WorkerExists() (bool, error) {
	if err := auth.EnsureValidToken(c.cfg); err != nil {
		return false, err
	}

	url := fmt.Sprintf("%s/scripts/edgessh", c.workersURL())
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+c.cfg.OAuthToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	return resp.StatusCode == 200, nil
}
