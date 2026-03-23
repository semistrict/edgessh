package workerapi

import "encoding/json"

type SessionInfo struct {
	SessionToken string `json:"session_token"`
	Subject      string `json:"sub"`
	Name         string `json:"name"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (c *Client) ExchangeVumelaToken(vumelaToken string) (*SessionInfo, error) {
	body, err := c.postJSON(c.WorkerURL+"/api/auth/exchange", map[string]string{
		"token": vumelaToken,
	})
	if err != nil {
		return nil, err
	}
	var info SessionInfo
	return &info, json.Unmarshal(body, &info)
}

func (c *Client) ExchangeSharedSecret(sharedSecret string) (*SessionInfo, error) {
	body, err := c.postJSON(c.WorkerURL+"/api/auth/exchange-shared", map[string]string{
		"shared_secret": sharedSecret,
	})
	if err != nil {
		return nil, err
	}
	var info SessionInfo
	return &info, json.Unmarshal(body, &info)
}

func (c *Client) Me() (*SessionInfo, error) {
	var info SessionInfo
	return &info, c.getJSON(c.WorkerURL+"/api/auth/me", &info)
}
