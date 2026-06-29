// Package config holds the deploy configuration and persists it (0600) under
// ~/.config/pocketdev/config.json so re-runs remember your choices.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Config is everything needed to provision a box. Secrets are stored too, so the
// file is written with 0600 perms.
type Config struct {
	Agents        []string `json:"agents"`
	Name          string   `json:"name"`
	Region        string   `json:"region"`
	ServerType    string   `json:"server_type"`
	WithoutIPv4   bool     `json:"without_ipv4"`
	RebootTime    string   `json:"reboot_time"`
	TSTag         string   `json:"ts_tag"`
	SSHPublicKeys []string `json:"ssh_public_keys"`

	Source    string `json:"source"`     // "github" | "rsync" | "fresh"
	GitHub    bool   `json:"github"`     // set up gh + clone on the box (source=github)
	RepoURL   string `json:"repo_url"`   // repo to clone (owner/repo or URL)
	LocalPath string `json:"local_path"` // local folder/file to rsync (source=rsync)

	SSHIDHandle string `json:"sshid_handle"` // Termius SSH ID handle; its public keys are added to the box for keyless mobile

	UseExisting    bool   `json:"use_existing"`    // adopt a server you already own instead of creating
	ExistingServer string `json:"existing_server"` // Hetzner server name chosen to adopt
	SSHHost        string `json:"ssh_host"`        // IPv4 or tailnet/MagicDNS name to reach an adopted box
	SSHUser        string `json:"ssh_user"`        // login user for adoption; default "root"
	Category       string `json:"category"`        // selected size category (create path)

	HCloudToken   string `json:"hcloud_token"`
	TSOAuthID     string `json:"ts_oauth_client_id"`
	TSOAuthSecret string `json:"ts_oauth_client_secret"`
	TSAuthKey     string `json:"ts_authkey"`
}

// Defaults returns a config pre-filled with sensible values and any matching
// environment variables (env wins over the blank defaults).
func Defaults() Config {
	c := Config{
		Agents:     []string{"claude"},
		Name:       "devbox",
		Region:     "nbg1",
		ServerType: "cax21",
		RebootTime: "05:00",
		TSTag:      "tag:devbox",
		SSHUser:    "root",
	}
	applyEnv(&c)
	return c
}

// applyEnv overlays the credential env vars onto c (env always wins).
func applyEnv(c *Config) {
	if v := os.Getenv("HCLOUD_TOKEN"); v != "" {
		c.HCloudToken = v
	}
	if v := os.Getenv("TS_OAUTH_CLIENT_ID"); v != "" {
		c.TSOAuthID = v
	}
	if v := os.Getenv("TS_OAUTH_CLIENT_SECRET"); v != "" {
		c.TSOAuthSecret = v
	}
	if v := os.Getenv("TS_AUTHKEY"); v != "" {
		c.TSAuthKey = v
	}
}

// Path is the on-disk config location.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pocketdev", "config.json"), nil
}

// Load reads the saved config, falling back to Defaults() if none exists.
// Saved values are overlaid on defaults; env vars still win for secrets.
func Load() (Config, error) {
	c := Defaults()
	p, err := Path()
	if err != nil {
		return c, nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return c, nil // no saved config yet
	}
	saved := c
	if err := json.Unmarshal(data, &saved); err != nil {
		return c, nil // ignore a corrupt file rather than block the user
	}
	// Re-apply env so exported credentials always override stale saved ones.
	applyEnv(&saved)
	return saved, nil
}

// HCloudCLIToken reads the active context's token from the official hcloud CLI
// config (~/.config/hcloud/cli.toml). Best-effort: returns ok=false on any miss,
// so users who already ran `hcloud context create` skip the token step.
func HCloudCLIToken() (token, contextName string, ok bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", false
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	data, err := os.ReadFile(filepath.Join(base, "hcloud", "cli.toml"))
	if err != nil {
		return "", "", false
	}

	type ctxEntry struct{ name, token string }
	var ctxs []ctxEntry
	active := ""
	cur := -1
	for raw := range strings.SplitSeq(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "[[contexts]]" {
			ctxs = append(ctxs, ctxEntry{})
			cur = len(ctxs) - 1
			continue
		}
		if strings.HasPrefix(line, "[") { // any other table ends context-key scope
			cur = -1
			continue
		}
		key, val, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"`)
		switch key {
		case "active_context":
			active = val
		case "name":
			if cur >= 0 {
				ctxs[cur].name = val
			}
		case "token":
			if cur >= 0 {
				ctxs[cur].token = val
			}
		}
	}
	for _, c := range ctxs {
		if c.name == active && c.token != "" {
			return c.token, c.name, true
		}
	}
	if len(ctxs) == 1 && ctxs[0].token != "" { // single context, no explicit active
		return ctxs[0].token, ctxs[0].name, true
	}
	return "", "", false
}

// Save persists the config with 0600 perms.
func Save(c Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}
