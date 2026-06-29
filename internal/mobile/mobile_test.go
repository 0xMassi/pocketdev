package mobile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHURL(t *testing.T) {
	if got := (Box{User: "dev", FQDN: "devbox.tail.ts.net"}).SSHURL(); got != "ssh://dev@devbox.tail.ts.net" {
		t.Errorf("FQDN url = %q", got)
	}
	if got := (Box{User: "dev", IP: "100.0.0.1"}).SSHURL(); got != "ssh://dev@100.0.0.1" {
		t.Errorf("IP-fallback url = %q", got)
	}
}

func TestSSHConfigIdempotentAndScoped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, ".ssh", "config")
	// Pre-existing unrelated host must survive.
	if err := os.WriteFile(cfg, []byte("Host work\n    HostName work.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	b := Box{Name: "devbox", FQDN: "devbox.tail.ts.net", IP: "100.0.0.1", User: "dev", KeyPath: "/k"}
	p, err := WriteSSHConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSSHConfig(b); err != nil { // write again: must not duplicate
		t.Fatal(err)
	}
	s := readFile(t, p)
	if n := strings.Count(s, "# >>> pocketdev devbox"); n != 1 {
		t.Errorf("block appears %d times, want 1 (not idempotent)", n)
	}
	if !strings.Contains(s, "Host work") {
		t.Error("clobbered the unrelated Host work")
	}
	if !strings.Contains(s, "HostName devbox.tail.ts.net") {
		t.Error("missing the box HostName")
	}

	if err := RemoveSSHConfig("devbox"); err != nil {
		t.Fatal(err)
	}
	s = readFile(t, p)
	if strings.Contains(s, "pocketdev devbox") {
		t.Error("block not removed")
	}
	if !strings.Contains(s, "Host work") {
		t.Error("RemoveSSHConfig removed the unrelated host")
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
