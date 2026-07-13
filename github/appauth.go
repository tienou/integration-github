package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// appAuth implements GitHub App authentication: a short-lived JWT signed
// with the App private key is exchanged for an installation access token
// (valid one hour), transparently refreshed before it expires.
type appAuth struct {
	appID          string
	installationID string
	key            *rsa.PrivateKey
	apiURL         string
	owner          string
	client         *http.Client

	mu     sync.Mutex
	token  string
	expiry time.Time
}

func newAppAuth(appID, installationID, pemData, apiURL, owner string, client *http.Client) (*appAuth, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, fmt.Errorf("private_key: no PEM block found")
	}

	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	} else if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("private_key: not an RSA key")
		}
		key = rsaKey
	} else {
		return nil, fmt.Errorf("private_key: unsupported key format")
	}

	return &appAuth{
		appID:          appID,
		installationID: installationID,
		key:            key,
		apiURL:         apiURL,
		owner:          owner,
		client:         client,
	}, nil
}

func b64url(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// jwt builds a RS256-signed JWT identifying the App, valid 9 minutes
// (GitHub caps at 10, and allows 60s of clock drift backwards).
func (a *appAuth) jwt() (string, error) {
	now := time.Now()
	header := b64url([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": a.appID,
	})
	signing := header + "." + b64url(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("signing app JWT: %w", err)
	}
	return signing + "." + b64url(sig), nil
}

func (a *appAuth) appRequest(ctx context.Context, method, path, jwt string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.apiURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+jwt)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("github app api %s: %s: %s", path, resp.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// discoverInstallation finds the App installation matching the configured
// owner (organization or user).
func (a *appAuth) discoverInstallation(ctx context.Context, jwt string) (string, error) {
	for page := 1; ; page++ {
		data, err := a.appRequest(ctx, http.MethodGet,
			fmt.Sprintf("/app/installations?per_page=100&page=%d", page), jwt, nil)
		if err != nil {
			return "", err
		}
		var installations []struct {
			Id      int64 `json:"id"`
			Account struct {
				Login string `json:"login"`
			} `json:"account"`
		}
		if err := json.Unmarshal(data, &installations); err != nil {
			return "", err
		}
		for _, inst := range installations {
			if strings.EqualFold(inst.Account.Login, a.owner) {
				return fmt.Sprint(inst.Id), nil
			}
		}
		if len(installations) < 100 {
			return "", fmt.Errorf("github app %s has no installation on %q — install the app on the organization first", a.appID, a.owner)
		}
	}
}

// currentToken returns a valid installation token, requesting or
// refreshing it when needed. Safe for concurrent use.
func (a *appAuth) currentToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.token != "" && time.Until(a.expiry) > 5*time.Minute {
		return a.token, nil
	}

	jwt, err := a.jwt()
	if err != nil {
		return "", err
	}

	if a.installationID == "" {
		id, err := a.discoverInstallation(ctx, jwt)
		if err != nil {
			return "", err
		}
		a.installationID = id
	}

	data, err := a.appRequest(ctx, http.MethodPost,
		"/app/installations/"+a.installationID+"/access_tokens", jwt, bytes.NewReader(nil))
	if err != nil {
		return "", err
	}

	var res struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", err
	}
	if res.Token == "" {
		return "", fmt.Errorf("github app: empty installation token")
	}

	a.token = res.Token
	a.expiry = res.ExpiresAt
	return a.token, nil
}
