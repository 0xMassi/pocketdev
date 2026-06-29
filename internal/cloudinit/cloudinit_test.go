package cloudinit

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/0xMassi/pocketdev/internal/agents"
	"gopkg.in/yaml.v3"
)

func TestRenderValidYAML(t *testing.T) {
	ag, err := agents.Resolve([]string{"claude", "codex", "cursor", "gemini", "aider"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Render(Params{
		Hostname:   "devbox",
		Tag:        "tag:devbox",
		TSAuthKey:  "tskey-auth-test",
		RebootTime: "05:00",
		SSHKeys:    []string{"ssh-ed25519 AAAATEST laptop", "ssh-ed25519 AAAATEST2 phone"},
		Agents:     ag,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(out, "#cloud-config\n") {
		t.Fatalf("missing #cloud-config header")
	}

	// The body after the header must parse as YAML.
	body := strings.TrimPrefix(out, "#cloud-config\n")
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("cloud-init is not valid YAML: %v", err)
	}

	for _, key := range []string{"users", "write_files", "runcmd", "packages"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("cloud-config missing %q", key)
		}
	}

	// The sys-setup + helper scripts are base64-encoded in write_files; decode
	// them and assert the sys-deps (node/pipx/keyring) and per-agent login lines.
	sysSetupSh := decodeWriteFile(t, doc, "/opt/pocketdev/sys-setup.sh")
	for _, want := range []string{"nodejs", "pipx", "gnome-keyring"} {
		if !strings.Contains(sysSetupSh, want) {
			t.Errorf("expected sys-setup to install %q", want)
		}
	}
	helperSh := decodeWriteFile(t, doc, "/usr/local/bin/pocketdev")
	for _, a := range ag {
		if !strings.Contains(helperSh, "login_"+a.Key) {
			t.Errorf("on-box helper missing login_%s", a.Key)
		}
	}
}

// decodeWriteFile finds a base64 write_files entry by path and returns its decoded content.
func decodeWriteFile(t *testing.T, doc map[string]any, path string) string {
	t.Helper()
	wf, ok := doc["write_files"].([]any)
	if !ok {
		t.Fatalf("write_files not a list")
	}
	for _, e := range wf {
		m, ok := e.(map[string]any)
		if !ok || m["path"] != path {
			continue
		}
		raw, _ := m["content"].(string)
		dec, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			t.Fatalf("write_file %s not valid base64: %v", path, err)
		}
		return string(dec)
	}
	t.Fatalf("write_file %s not found", path)
	return ""
}

func TestRenderBootstrap(t *testing.T) {
	ag, _ := agents.Resolve([]string{"claude", "gemini"})
	s := RenderBootstrap(Params{
		Hostname: "mybox", Tag: "tag:devbox", TSAuthKey: "tskey-x",
		Agents: ag, GitHub: true, RepoURL: "0xMassi/pocketdev", AdoptUser: "ubuntu",
	})
	for _, want := range []string{
		"#!/usr/bin/env bash",
		"command -v apt-get",                       // Debian-only guard
		`tailscale up --authkey "tskey-x"`,         // joins tailnet
		"base64 -d > /usr/local/bin/pocketdev",     // drops the helper
		"base64 -d > /opt/pocketdev/sys-setup.sh",  // drops sys-setup
		"runuser -l ubuntu -c 'pocketdev install'", // installs as the login user
	} {
		if !strings.Contains(s, want) {
			t.Errorf("bootstrap missing %q", want)
		}
	}
	if strings.Contains(s, "ufw ") {
		t.Error("bootstrap must not run ufw (lockout risk on a public-IP SSH session)")
	}
}

func TestRenderNoSysDeps(t *testing.T) {
	ag, _ := agents.Resolve([]string{"claude"})
	out, err := Render(Params{Hostname: "x", Tag: "tag:devbox", RebootTime: "05:00", SSHKeys: []string{"k"}, Agents: ag})
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimPrefix(out, "#cloud-config\n")
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("invalid YAML: %v", err)
	}
}
