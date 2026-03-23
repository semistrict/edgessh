// Package workerapi provides a client for the edgessh Worker API.
// This talks to our Worker (DO scheduler + container proxy), not the Cloudflare API.
package workerapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type Client struct {
	WorkerURL  string
	AuthToken  string
	httpClient *http.Client
}

func NewClient(workerURL, authToken string) *Client {
	return &Client{
		WorkerURL:  workerURL,
		AuthToken:  authToken,
		httpClient: http.DefaultClient,
	}
}

func (c *Client) do(method, url string, requestBody io.Reader, contentType string) ([]byte, error) {
	req, err := http.NewRequest(method, url, requestBody)
	if err != nil {
		return nil, err
	}
	if c.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AuthToken)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s", string(responseBody))
	}
	return responseBody, nil
}

func (c *Client) get(url string) ([]byte, error) {
	return c.do(http.MethodGet, url, nil, "")
}

func (c *Client) post(url string) ([]byte, error) {
	return c.do(http.MethodPost, url, nil, "")
}

func (c *Client) postJSON(url string, payload interface{}) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return c.do(http.MethodPost, url, bytes.NewReader(data), "application/json")
}

func (c *Client) getJSON(url string, dst interface{}) error {
	body, err := c.get(url)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}
