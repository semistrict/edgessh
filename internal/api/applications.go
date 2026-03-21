package api

import "fmt"

type Application struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Configuration  ApplicationConfig `json:"configuration"`
	DurableObjects *DOConfig         `json:"durable_objects,omitempty"`
	MaxInstances   int               `json:"max_instances"`
}

type ApplicationConfig struct {
	Image          string             `json:"image"`
	InstanceType   string             `json:"instance_type,omitempty"`
	WranglerSSH    *WranglerSSHConfig `json:"wrangler_ssh,omitempty"`
	AuthorizedKeys []AuthorizedKey    `json:"authorized_keys,omitempty"`
}

type WranglerSSHConfig struct {
	Enabled bool `json:"enabled"`
}

type AuthorizedKey struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

type DOConfig struct {
	NamespaceID string `json:"namespace_id"`
}

type CreateApplicationRequest struct {
	Name             string            `json:"name"`
	Configuration    ApplicationConfig `json:"configuration"`
	MaxInstances     int               `json:"max_instances"`
	Instances        int               `json:"instances"`
	SchedulingPolicy string            `json:"scheduling_policy"`
	DurableObjects   *DOConfig         `json:"durable_objects,omitempty"`
}

type DashInstance struct {
	ID               string `json:"id"`
	Name             string `json:"name,omitempty"`
	CurrentPlacement *struct {
		Status *struct {
			ContainerStatus string `json:"container_status,omitempty"`
			Health          string `json:"health,omitempty"`
		} `json:"status,omitempty"`
	} `json:"current_placement,omitempty"`
}

type DashDOInstance struct {
	ID           string `json:"id"`
	DeploymentID string `json:"deployment_id,omitempty"`
	Name         string `json:"name,omitempty"`
}

type DashInstancesResponse struct {
	Instances      []DashInstance   `json:"instances"`
	DurableObjects []DashDOInstance `json:"durable_objects,omitempty"`
}

func (c *Client) CreateApplication(req *CreateApplicationRequest) (*Application, error) {
	var app Application
	if err := c.doPOST(c.containersURL()+"/applications", req, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

func (c *Client) ListApplications() ([]Application, error) {
	var apps []Application
	if err := c.doGET(c.containersURL()+"/applications", &apps); err != nil {
		return nil, err
	}
	return apps, nil
}

func (c *Client) GetApplicationByName(name string) (*Application, error) {
	apps, err := c.ListApplications()
	if err != nil {
		return nil, err
	}
	for _, app := range apps {
		if app.Name == name {
			return &app, nil
		}
	}
	return nil, fmt.Errorf("application %q not found", name)
}

func (c *Client) DeleteApplication(appID string) error {
	return c.doDELETE(c.containersURL() + "/applications/" + appID)
}

func (c *Client) ListInstances(appID string) (*DashInstancesResponse, error) {
	var resp DashInstancesResponse
	if err := c.doGET(fmt.Sprintf("%s/dash/applications/%s/instances?per_page=100", c.containersURL(), appID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ResolveInstanceID finds the 64-char hex DO instance ID for a named container.
// The SSH endpoint requires this DO ID, not the deployment UUID.
// Flow: list instances → find DO with matching name → return DO's hex ID.
func (c *Client) ResolveInstanceID(appID, name string) (string, error) {
	resp, err := c.ListInstances(appID)
	if err != nil {
		return "", err
	}

	// Find the DO with this name — its ID is the 64-char hex needed for SSH
	for _, do := range resp.DurableObjects {
		if do.Name == name {
			return do.ID, nil
		}
	}

	// Fallback: if there's only one DO, use it
	if len(resp.DurableObjects) == 1 {
		return resp.DurableObjects[0].ID, nil
	}

	return "", fmt.Errorf("no running instance found for %q", name)
}
