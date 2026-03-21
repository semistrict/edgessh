package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/anthropics/edgessh/internal/config"
)

const (
	// Same client ID as wrangler
	ClientID    = "54d11594-84e4-41aa-b438-e81b8fa78ee7"
	AuthURL     = "https://dash.cloudflare.com/oauth2/auth"
	TokenURL    = "https://dash.cloudflare.com/oauth2/token"
	CallbackURL = "http://localhost:8976/oauth/callback"

	// Scopes needed for edgessh
	Scopes = "account:read user:read workers:write workers_scripts:write containers:write secrets_store:write offline_access"
)

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error,omitempty"`
}

// Login performs the OAuth2 PKCE flow identical to wrangler's login.
func Login() (*config.Config, error) {
	codeVerifier, codeChallenge, err := generatePKCE()
	if err != nil {
		return nil, fmt.Errorf("generating PKCE: %w", err)
	}

	state := generateState(32)

	authURL := fmt.Sprintf("%s?response_type=code&client_id=%s&redirect_uri=%s&scope=%s&state=%s&code_challenge=%s&code_challenge_method=S256",
		AuthURL,
		url.QueryEscape(ClientID),
		url.QueryEscape(CallbackURL),
		url.QueryEscape(Scopes),
		state,
		url.QueryEscape(codeChallenge),
	)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}

		q := r.URL.Query()

		if errVal := q.Get("error"); errVal != "" {
			errCh <- fmt.Errorf("oauth error: %s", errVal)
			http.Redirect(w, r, "https://welcome.developers.workers.dev/wrangler-oauth-consent-denied", http.StatusTemporaryRedirect)
			return
		}

		code := q.Get("code")
		if code == "" {
			errCh <- fmt.Errorf("no authorization code received")
			return
		}

		returnedState := q.Get("state")
		if returnedState != state {
			errCh <- fmt.Errorf("state mismatch")
			return
		}

		codeCh <- code
		http.Redirect(w, r, "https://welcome.developers.workers.dev/wrangler-oauth-consent-granted", http.StatusTemporaryRedirect)
	})

	listener, err := net.Listen("tcp", "localhost:8976")
	if err != nil {
		return nil, fmt.Errorf("starting callback server: %w", err)
	}

	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Shutdown(context.Background())

	fmt.Printf("Opening browser to login...\n")
	fmt.Printf("If the browser doesn't open, visit:\n%s\n\n", authURL)
	openBrowser(authURL)

	// Wait for callback or timeout
	var authCode string
	select {
	case authCode = <-codeCh:
	case err := <-errCh:
		return nil, err
	case <-time.After(120 * time.Second):
		return nil, fmt.Errorf("timed out waiting for authorization")
	}

	// Exchange code for token
	token, err := exchangeCodeForToken(authCode, codeVerifier)
	if err != nil {
		return nil, err
	}

	// Get account ID
	accountID, err := getAccountID(token.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("getting account ID: %w", err)
	}

	expiry := time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)

	cfg := &config.Config{
		OAuthToken:   token.AccessToken,
		RefreshToken: token.RefreshToken,
		Expiry:       expiry.Format(time.RFC3339),
		AccountID:    accountID,
	}

	return cfg, nil
}

// RefreshAccessToken refreshes the OAuth token using the refresh token.
func RefreshAccessToken(cfg *config.Config) error {
	params := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {cfg.RefreshToken},
		"client_id":     {ClientID},
	}

	resp, err := http.PostForm(TokenURL, params)
	if err != nil {
		return fmt.Errorf("refreshing token: %w", err)
	}
	defer resp.Body.Close()

	var token TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return fmt.Errorf("decoding token response: %w", err)
	}

	if token.Error != "" {
		return fmt.Errorf("token refresh error: %s", token.Error)
	}

	cfg.OAuthToken = token.AccessToken
	if token.RefreshToken != "" {
		cfg.RefreshToken = token.RefreshToken
	}
	cfg.Expiry = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Format(time.RFC3339)

	return config.Save(cfg)
}

// EnsureValidToken checks if the token is expired and refreshes if needed.
func EnsureValidToken(cfg *config.Config) error {
	if !cfg.IsTokenExpired() {
		return nil
	}
	if cfg.RefreshToken == "" {
		return fmt.Errorf("token expired, run 'edgessh login' again")
	}
	fmt.Println("Token expired, refreshing...")
	return RefreshAccessToken(cfg)
}

func exchangeCodeForToken(code, codeVerifier string) (*TokenResponse, error) {
	params := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {CallbackURL},
		"client_id":     {ClientID},
		"code_verifier": {codeVerifier},
	}

	resp, err := http.PostForm(TokenURL, params)
	if err != nil {
		return nil, fmt.Errorf("exchanging code: %w", err)
	}
	defer resp.Body.Close()

	var token TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("decoding token: %w", err)
	}

	if token.Error != "" {
		return nil, fmt.Errorf("token error: %s", token.Error)
	}

	return &token, nil
}

type accountsResponse struct {
	Result []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"result"`
	Success bool `json:"success"`
}

func getAccountID(accessToken string) (string, error) {
	req, _ := http.NewRequest("GET", "https://api.cloudflare.com/client/v4/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var accounts accountsResponse
	if err := json.NewDecoder(resp.Body).Decode(&accounts); err != nil {
		return "", err
	}

	if !accounts.Success || len(accounts.Result) == 0 {
		return "", fmt.Errorf("no accounts found")
	}

	if len(accounts.Result) == 1 {
		return accounts.Result[0].ID, nil
	}

	fmt.Println("Multiple accounts found:")
	for i, a := range accounts.Result {
		fmt.Printf("  [%d] %s (%s)\n", i+1, a.Name, a.ID)
	}
	fmt.Print("Select account [1]: ")
	var choice int
	fmt.Scanln(&choice)
	if choice < 1 || choice > len(accounts.Result) {
		choice = 1
	}
	return accounts.Result[choice-1].ID, nil
}

// generatePKCE generates a PKCE code_verifier and code_challenge (S256).
// Matches wrangler's implementation exactly.
func generatePKCE() (verifier, challenge string, err error) {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	const verifierLength = 96

	// Generate random bytes and map to charset (same as wrangler)
	buf := make([]byte, verifierLength)
	for i := range buf {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", "", err
		}
		buf[i] = charset[n.Int64()]
	}
	rawVerifier := string(buf)
	// base64url-encode the verifier (wrangler does this)
	verifier = base64urlEncode(rawVerifier)

	// SHA-256 hash of verifier, then base64url-encode
	hash := sha256.Sum256([]byte(verifier))
	challenge = base64urlEncode(string(hash[:]))

	return verifier, challenge, nil
}

func base64urlEncode(s string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(s))
	encoded = strings.ReplaceAll(encoded, "+", "-")
	encoded = strings.ReplaceAll(encoded, "/", "_")
	encoded = strings.TrimRight(encoded, "=")
	return encoded
}

func generateState(length int) string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, length)
	for i := range buf {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		buf[i] = charset[n.Int64()]
	}
	return string(buf)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	cmd.Start()
}
