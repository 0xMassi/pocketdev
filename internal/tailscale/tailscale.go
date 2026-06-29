// Package tailscale mints short-lived, tagged auth keys from an OAuth client and
// watches for the new node to join the tailnet. No long-lived secret touches the box.
package tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const api = "https://api.tailscale.com/api/v2"

// httpClient bounds every call so a stalled API response can't hang the TUI
// (the root context carries no deadline).
var httpClient = &http.Client{Timeout: 30 * time.Second}

// MintKey exchanges an OAuth client for an access token, then creates a single-use,
// pre-authorized, tagged, non-ephemeral auth key (30 min TTL). It returns the key
// (for cloud-init) and the access token (reused to poll/revoke the node).
func MintKey(ctx context.Context, oauthID, oauthSecret, tag string) (key, accessToken string, err error) {
	accessToken, err = token(ctx, oauthID, oauthSecret)
	if err != nil {
		return "", "", err
	}
	body := fmt.Sprintf(`{"capabilities":{"devices":{"create":{"reusable":false,"ephemeral":false,"preauthorized":true,"tags":[%q]}}},"expirySeconds":1800,"description":"pocketdev"}`, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api+"/tailnet/-/keys", strings.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		Key string `json:"key"`
	}
	if err := do(req, &out); err != nil {
		return "", "", fmt.Errorf("mint auth key (OAuth client must own %s and hold the auth_keys write scope): %w", tag, err)
	}
	if out.Key == "" {
		return "", "", fmt.Errorf("tailscale returned an empty auth key")
	}
	return out.Key, accessToken, nil
}

// Validate confirms the OAuth client credentials work.
func Validate(ctx context.Context, oauthID, oauthSecret string) error {
	_, err := token(ctx, oauthID, oauthSecret)
	return err
}

// MintAccess exchanges an OAuth client for a short-lived API access token,
// used to revoke a node during teardown.
func MintAccess(ctx context.Context, oauthID, oauthSecret string) (string, error) {
	return token(ctx, oauthID, oauthSecret)
}

func token(ctx context.Context, id, secret string) (string, error) {
	form := url.Values{"client_id": {id}, "client_secret": {secret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := do(req, &out); err != nil {
		return "", fmt.Errorf("tailscale OAuth token exchange: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("tailscale OAuth returned no access token (check client id/secret)")
	}
	return out.AccessToken, nil
}

type device struct {
	ID        string   `json:"id"`
	Hostname  string   `json:"hostname"`
	Addresses []string `json:"addresses"`
	Created   string   `json:"created"`
}

func devices(ctx context.Context, accessToken string) ([]device, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/tailnet/-/devices", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var out struct {
		Devices []device `json:"devices"`
	}
	if err := do(req, &out); err != nil {
		return nil, err
	}
	return out.Devices, nil
}

// RevokeByHostname deletes every tailnet node with the given hostname. Tagged
// nodes never expire, so an old box must be revoked explicitly on rebuild/teardown.
func RevokeByHostname(ctx context.Context, accessToken, hostname string) error {
	devs, err := devices(ctx, accessToken)
	if err != nil {
		return err
	}
	for _, d := range devs {
		if d.Hostname != hostname {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, api+"/device/"+d.ID, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		if err := do(req, nil); err != nil {
			return err
		}
	}
	return nil
}

// WaitForNode polls until a node with the given hostname appears, returning its
// 100.x address. It picks the most recently created match to avoid stale nodes.
func WaitForNode(ctx context.Context, accessToken, hostname string, attempts int, every time.Duration) (string, error) {
	for range attempts {
		devs, err := devices(ctx, accessToken)
		if err == nil {
			var newest device
			var newestAt time.Time
			for _, d := range devs {
				if d.Hostname != hostname {
					continue
				}
				at, _ := time.Parse(time.RFC3339, d.Created)
				if newest.ID == "" || at.After(newestAt) {
					newest, newestAt = d, at
				}
			}
			for _, a := range newest.Addresses {
				if strings.HasPrefix(a, "100.") {
					return a, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(every):
		}
	}
	return "", fmt.Errorf("node %q did not join the tailnet in time", hostname)
}

func do(req *http.Request, out any) error {
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("tailscale API %d: %s", resp.StatusCode, bytes.TrimSpace(data))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}
