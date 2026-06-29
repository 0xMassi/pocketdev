package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCLI(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "hcloud"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hcloud", "cli.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHCloudCLIToken_PicksActiveContext(t *testing.T) {
	writeCLI(t, `active_context = "prod"

[[contexts]]
name = "dev"
token = "tok-dev"

[[contexts]]
name = "prod"
token = "tok-prod"
`)
	tok, name, ok := HCloudCLIToken()
	if !ok || tok != "tok-prod" || name != "prod" {
		t.Fatalf("got token=%q name=%q ok=%v; want tok-prod/prod/true", tok, name, ok)
	}
}

func TestHCloudCLIToken_SingleContextFallback(t *testing.T) {
	writeCLI(t, `[[contexts]]
name = "only"
token = "tok-only"
`)
	tok, _, ok := HCloudCLIToken()
	if !ok || tok != "tok-only" {
		t.Fatalf("got token=%q ok=%v; want tok-only/true", tok, ok)
	}
}

func TestHCloudCLIToken_Missing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no cli.toml inside
	if _, _, ok := HCloudCLIToken(); ok {
		t.Fatal("expected ok=false when cli.toml is absent")
	}
}
