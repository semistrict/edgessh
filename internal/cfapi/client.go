package cfapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/anthropics/edgessh/internal/config"
)

const BaseAPIURL = "https://api.cloudflare.com/client/v4"

type Client struct {
	cfg        *config.Config
	httpClient *http.Client
}

func NewClient(cfg *config.Config) *Client {
	return &Client{cfg: cfg, httpClient: http.DefaultClient}
}

// GetAccountID fetches the account ID from the Cloudflare API.
func (c *Client) GetAccountID() (string, error) {
	var accounts []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := c.doGET(BaseAPIURL+"/accounts?per_page=5", &accounts); err != nil {
		return "", err
	}
	if len(accounts) == 0 {
		return "", fmt.Errorf("no accounts found")
	}
	if len(accounts) == 1 {
		return accounts[0].ID, nil
	}
	fmt.Println("Multiple accounts found:")
	for i, a := range accounts {
		fmt.Printf("  [%d] %s (%s)\n", i+1, a.Name, a.ID)
	}
	fmt.Print("Select account [1]: ")
	var choice int
	fmt.Scanln(&choice)
	if choice < 1 || choice > len(accounts) {
		choice = 1
	}
	return accounts[choice-1].ID, nil
}

func (c *Client) containersURL() string {
	return fmt.Sprintf("%s/accounts/%s/containers", BaseAPIURL, c.cfg.AccountID)
}

func (c *Client) workersURL() string {
	return fmt.Sprintf("%s/accounts/%s/workers", BaseAPIURL, c.cfg.AccountID)
}

// doGET makes a GET request and unmarshals the result.
func (c *Client) doGET(url string, result interface{}) error {
	return c.do("GET", url, nil, "", result)
}

// doPOST makes a POST request with a JSON body and unmarshals the result.
func (c *Client) doPOST(url string, body interface{}, result interface{}) error {
	return c.doJSONBody("POST", url, body, result)
}

// doPATCH makes a PATCH request with a JSON body and unmarshals the result.
func (c *Client) doPATCH(url string, body interface{}, result interface{}) error {
	return c.doJSONBody("PATCH", url, body, result)
}

// doPUT makes a PUT request with a raw body and content type.
func (c *Client) doPUT(url string, body io.Reader, contentType string, result interface{}) error {
	return c.do("PUT", url, body, contentType, result)
}

// doDELETE makes a DELETE request.
func (c *Client) doDELETE(url string) error {
	return c.do("DELETE", url, nil, "", nil)
}

// doJSONBody marshals body as JSON and sends with the given method.
func (c *Client) doJSONBody(method, url string, body interface{}, result interface{}) error {
	var reader io.Reader
	ct := ""
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
		ct = "application/json"
	}
	return c.do(method, url, reader, ct, result)
}

// do is the core HTTP method. Sends a request, reads the response, unwraps
// Cloudflare's {success, result, errors} envelope, and unmarshals into result.
func (c *Client) do(method, url string, body io.Reader, contentType string, result interface{}) error {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.BearerToken())
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	if result == nil {
		return nil
	}

	return unmarshalCF(respBody, result)
}

// unmarshalCF handles Cloudflare's envelope format: {success, result, errors, messages}.
// If the response is wrapped, it unwraps "result" into dst.
// If success is false with errors, returns an error with all details.
// If the response isn't wrapped, falls back to direct unmarshal.
func unmarshalCF(data []byte, dst interface{}) error {
	var envelope struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
		Errors  []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(data, &envelope); err == nil && (envelope.Result != nil || len(envelope.Errors) > 0) {
		if !envelope.Success && len(envelope.Errors) > 0 {
			msgs := make([]string, len(envelope.Errors))
			for i, e := range envelope.Errors {
				msgs[i] = fmt.Sprintf("[%d] %s", e.Code, e.Message)
			}
			return fmt.Errorf("API errors: %s", strings.Join(msgs, "; "))
		}
		if envelope.Result != nil {
			return json.Unmarshal(envelope.Result, dst)
		}
	}

	return json.Unmarshal(data, dst)
}
