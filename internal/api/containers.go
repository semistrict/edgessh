package api

import (
	"fmt"
	"io"
	"net/http"
)

// WakeContainer sends a request to the Worker to spin up a named DO instance.
// The Worker routes /{name}/start → idFromName(name) → container.start().
func (c *Client) WakeContainer(workerURL, name string) error {
	url := fmt.Sprintf("%s/%s/start", workerURL, name)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("waking container: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("waking container: %d %s", resp.StatusCode, string(body))
	}

	fmt.Printf("Worker: %s\n", string(body))
	return nil
}

// StopContainer sends a stop request to the named container via the Worker.
func (c *Client) StopContainer(workerURL, name string) error {
	url := fmt.Sprintf("%s/%s/stop", workerURL, name)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("stopping container: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("stopping container: %d %s", resp.StatusCode, string(body))
	}

	fmt.Printf("Stopped: %s\n", string(body))
	return nil
}
