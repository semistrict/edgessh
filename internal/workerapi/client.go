// Package workerapi provides a client for the edgessh Worker API.
// This talks to our Worker (DO scheduler + container proxy), not the Cloudflare API.
package workerapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type Client struct {
	WorkerURL  string
	httpClient *http.Client
}

func NewClient(workerURL string) *Client {
	return &Client{
		WorkerURL:  workerURL,
		httpClient: http.DefaultClient,
	}
}

func (c *Client) get(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s", string(body))
	}
	return body, nil
}

func (c *Client) post(url string) ([]byte, error) {
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s", string(body))
	}
	return body, nil
}

func (c *Client) getJSON(url string, dst interface{}) error {
	body, err := c.get(url)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}
