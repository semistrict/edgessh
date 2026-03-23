package workerapi

import "fmt"

type ContainerInfo struct {
	ID      string `json:"id"`
	DOName  string `json:"do_name"`
	VMCount int    `json:"vm_count"`
	MaxVMs  int    `json:"max_vms"`
}

func (c *Client) ListContainers() ([]ContainerInfo, error) {
	var containers []ContainerInfo
	return containers, c.getJSON(c.WorkerURL+"/api/container/list", &containers)
}

func (c *Client) WakeContainer(name string) error {
	_, err := c.get(fmt.Sprintf("%s/%s/start", c.WorkerURL, name))
	return err
}

func (c *Client) StopContainer(name string) error {
	_, err := c.get(fmt.Sprintf("%s/%s/stop", c.WorkerURL, name))
	return err
}
