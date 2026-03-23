package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

type Config struct {
	// Master API token — only used to mint scoped tokens during setup.
	// Needs "User > API Tokens > Edit" permission.
	MasterToken string `json:"master_token"`
	AccountID   string `json:"account_id"`

	// Scoped API token for Workers/Containers management (minted during setup)
	APIToken string `json:"api_token,omitempty"`

	// Set after `edgessh setup`
	DONamespaceID    string `json:"do_namespace_id,omitempty"`
	ApplicationID    string `json:"application_id,omitempty"`
	WorkerURL        string `json:"worker_url,omitempty"`
	WorkerAuthSecret string `json:"worker_auth_secret,omitempty"`
	SessionToken     string `json:"session_token,omitempty"`
	SessionSubject   string `json:"session_subject,omitempty"`
	SessionName      string `json:"session_name,omitempty"`

	// Loophole store URL for R2-backed VM rootfs volumes
	LoopholeStoreURL string `json:"loophole_store_url,omitempty"`
	// R2 S3 credentials for loophole (minted during setup)
	R2AccessKeyID     string `json:"r2_access_key_id,omitempty"`
	R2SecretAccessKey string `json:"r2_secret_access_key,omitempty"`
}

// BearerToken returns the scoped API token for day-to-day API calls.
// Falls back to master token if scoped token not yet created.
func (c *Config) BearerToken() string {
	if c.APIToken != "" {
		return c.APIToken
	}
	return c.MasterToken
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
		return nil, fmt.Errorf("not configured, run 'edgessh setup' first: %w", err)
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

func EnsureWorkerAuthSecret(cfg *Config) error {
	if cfg.WorkerAuthSecret != "" {
		return nil
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return err
	}
	cfg.WorkerAuthSecret = base64.RawURLEncoding.EncodeToString(raw[:])
	return nil
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
