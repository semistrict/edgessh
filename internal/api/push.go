package api

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	edgesshEmbed "github.com/anthropics/edgessh/embed"
)

// PushImage loads the embedded Docker image and pushes it to the Cloudflare registry.
func (c *Client) PushImage(tag string) error {
	fmt.Println("Getting registry credentials...")
	creds, err := c.GenerateRegistryCredentials(true, false)
	if err != nil {
		return fmt.Errorf("getting registry credentials: %w", err)
	}

	fmt.Println("Loading embedded image...")
	img, err := tarball.Image(func() (io.ReadCloser, error) {
		gz, err := gzip.NewReader(bytes.NewReader(edgesshEmbed.Image))
		if err != nil {
			return nil, fmt.Errorf("decompressing image: %w", err)
		}
		return gz, nil
	}, nil)
	if err != nil {
		return fmt.Errorf("loading embedded image: %w", err)
	}

	dest := c.ImageRef(tag)
	ref, err := name.ParseReference(dest)
	if err != nil {
		return fmt.Errorf("parsing image reference: %w", err)
	}

	fmt.Printf("Pushing image to %s...\n", ref.String())

	auth := authn.FromConfig(authn.AuthConfig{
		Username: creds.Username,
		Password: creds.Password,
	})

	// Use the same transport settings as Docker: DisableKeepAlives prevents
	// TLS connection reuse which causes "tls: bad record MAC" errors on
	// large blob uploads to the Cloudflare registry.
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
	}

	if err := crane.Push(img, dest, crane.WithAuth(auth), crane.WithTransport(transport)); err != nil {
		return fmt.Errorf("pushing image: %w", err)
	}

	fmt.Println("Image pushed successfully.")
	return nil
}
