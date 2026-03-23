package workerapi

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

type VMInfo struct {
	Name        string `json:"name"`
	ContainerID string `json:"container_id"`
	DOName      string `json:"do_name"`
	Rootfs      string `json:"rootfs"`
	CreatedAt   string `json:"created_at"`
}

type SSHInfo struct {
	DOName      string `json:"do_name"`
	ContainerID string `json:"container_id"`
	Rootfs      string `json:"rootfs"`
}

func (c *Client) CreateVM(name, rootfs, sshPubKey string) (*VMInfo, error) {
	u := fmt.Sprintf("%s/api/vm/create?%s", c.WorkerURL,
		url.Values{"name": {name}, "rootfs": {rootfs}, "ssh_pubkey": {sshPubKey}}.Encode())
	body, err := c.post(u)
	if err != nil {
		return nil, err
	}
	var vm VMInfo
	return &vm, json.Unmarshal(body, &vm)
}

func (c *Client) ListVMs() ([]VMInfo, error) {
	var vms []VMInfo
	return vms, c.getJSON(c.WorkerURL+"/api/vm/list", &vms)
}

func (c *Client) StopVM(name string) error {
	u := fmt.Sprintf("%s/api/vm/stop?%s", c.WorkerURL,
		url.Values{"name": {name}}.Encode())
	_, err := c.post(u)
	return err
}

func (c *Client) DeleteVM(name string) error {
	u := fmt.Sprintf("%s/api/vm/delete?%s", c.WorkerURL,
		url.Values{"name": {name}}.Encode())
	_, err := c.post(u)
	return err
}

func (c *Client) GetVMSSHInfo(name, sshPubKey string) (*SSHInfo, error) {
	u := fmt.Sprintf("%s/api/vm/ssh-info?%s", c.WorkerURL,
		url.Values{"name": {name}, "ssh_pubkey": {sshPubKey}}.Encode())
	var info SSHInfo
	return &info, c.getJSON(u, &info)
}

func (c *Client) CheckpointVM(name string) (string, error) {
	u := fmt.Sprintf("%s/api/vm/checkpoint?%s", c.WorkerURL,
		url.Values{"name": {name}}.Encode())
	body, err := c.post(u)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

func (c *Client) GetVMStats(name string) (map[string]interface{}, error) {
	u := fmt.Sprintf("%s/api/vm/stats?%s", c.WorkerURL,
		url.Values{"name": {name}}.Encode())
	var stats map[string]interface{}
	return stats, c.getJSON(u, &stats)
}

