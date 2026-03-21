package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
)

type Config struct {
	OAuthToken   string `json:"oauth_token"`
	RefreshToken string `json:"refresh_token"`
	Expiry       string `json:"expiry"`
	AccountID    string `json:"account_id"`
	// Set after `edgessh setup`
	DONamespaceID string `json:"do_namespace_id,omitempty"`
	ApplicationID string `json:"application_id,omitempty"`
	WorkerURL     string `json:"worker_url,omitempty"`
}

func Dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".edgessh")
}

func Path() string {
	return filepath.Join(Dir(), "config.json")
}

func KeyDir() string {
	return filepath.Join(Dir(), "keys")
}

func PrivateKeyPath() string {
	return filepath.Join(KeyDir(), "id_ed25519")
}

func PublicKeyPath() string {
	return filepath.Join(KeyDir(), "id_ed25519.pub")
}

func Load() (*Config, error) {
	data, err := os.ReadFile(Path())
	if err != nil {
		return nil, fmt.Errorf("not logged in, run 'edgessh login' first: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func Save(cfg *Config) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), data, 0o600)
}

func (c *Config) IsTokenExpired() bool {
	if c.Expiry == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, c.Expiry)
	if err != nil {
		return true
	}
	return time.Now().After(t)
}

func GenerateKeyPair() error {
	if err := os.MkdirAll(KeyDir(), 0o700); err != nil {
		return err
	}

	if _, err := os.Stat(PrivateKeyPath()); err == nil {
		return nil
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}

	privBytes, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return err
	}
	if err := os.WriteFile(PrivateKeyPath(), pem.EncodeToMemory(privBytes), 0o600); err != nil {
		return err
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return err
	}
	pubBytes := ssh.MarshalAuthorizedKey(sshPub)
	return os.WriteFile(PublicKeyPath(), pubBytes, 0o644)
}

func ReadPublicKey() (string, error) {
	data, err := os.ReadFile(PublicKeyPath())
	if err != nil {
		return "", err
	}
	return string(data), nil
}
