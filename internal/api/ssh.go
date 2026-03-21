package api

import "fmt"

type SSHTunnelResponse struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

func (c *Client) GetSSHTunnel(instanceID string) (*SSHTunnelResponse, error) {
	var resp SSHTunnelResponse
	if err := c.doGET(fmt.Sprintf("%s/instances/%s/ssh", c.containersURL(), instanceID), &resp); err != nil {
		return nil, fmt.Errorf("getting SSH tunnel: %w", err)
	}
	return &resp, nil
}
