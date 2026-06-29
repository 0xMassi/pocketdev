package provision

import "testing"

func TestAllowedAuthURL(t *testing.T) {
	ok := []string{
		"https://github.com/login/device",
		"https://claude.ai/oauth/authorize?x=1",
		"https://accounts.google.com/o/oauth2/v2/auth",
		"http://login.tailscale.com/admin",
	}
	bad := []string{
		"https://evil.com/phish",
		"https://github.com.evil.com/x", // suffix-spoof must NOT pass
		"file:///etc/passwd",
		"javascript:alert(1)",
		"",
	}
	for _, u := range ok {
		if !allowedAuthURL(u) {
			t.Errorf("expected allowed: %q", u)
		}
	}
	for _, u := range bad {
		if allowedAuthURL(u) {
			t.Errorf("expected blocked: %q", u)
		}
	}
}

func TestValidPubKey(t *testing.T) {
	good := []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIabcdefghij phone",
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDexample",
		"ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTY no-comment",
	}
	for _, s := range good {
		if !ValidPubKey(s) {
			t.Errorf("ValidPubKey(%q) = false, want true", s)
		}
	}
	// Reject junk and, critically, shell/line metacharacters — the key is
	// single-quoted into a remote command, so these must never pass.
	bad := []string{
		"",
		"not a key",
		"ssh-ed25519",       // no body
		"ssh-ed25519 short", // body too short
		"telnet AAAAC3NzaC1lZDI1NTE5AAAAIabcdefghij x", // unknown type
		"ssh-ed25519 AAAA'; rm -rf ~ #",                // single quote -> breakout
		"ssh-ed25519 AAAAC3Nz\nrm -rf ~",               // newline injection
		"ssh-ed25519 AAAAC3Nz\\",                       // backslash
		`ssh-ed25519 AAAA" foo`,                        // double quote
	}
	for _, s := range bad {
		if ValidPubKey(s) {
			t.Errorf("ValidPubKey(%q) = true, want false", s)
		}
	}
}

func TestValidHandle(t *testing.T) {
	for _, s := range []string{"alex", "my-handle_1", "a.b.c", "ABC123"} {
		if !ValidHandle(s) {
			t.Errorf("ValidHandle(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "has space", "slash/inject", "semi;colon", "at@sign", "quote'", string(make([]byte, 65))} {
		if ValidHandle(s) {
			t.Errorf("ValidHandle(%q) = true, want false", s)
		}
	}
}

func TestParseAuthorizedKeys(t *testing.T) {
	// Real sshid.io shape: authorized_keys lines with a trailing comment, plus
	// blank lines and a stray comment line that must be ignored.
	body := "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTY= #ssh.id - @alex\n" +
		"\n" +
		"sk-ecdsa-sha2-nistp256@openssh.com AAAAInNrLWVjZHNhLXNoYTItb== #ssh.id - @alex\n" +
		"# just a comment, not a key\n" +
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDA5YCyXGFdUVAuEgSSbdx0c6\n"
	got := ParseAuthorizedKeys(body)
	if len(got) != 3 {
		t.Fatalf("ParseAuthorizedKeys returned %d keys, want 3: %v", len(got), got)
	}
	if got[0] != "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTY= #ssh.id - @alex" {
		t.Errorf("first key not preserved verbatim (comment dropped?): %q", got[0])
	}
	// The HTML fallback page must yield zero keys (so FetchSSHIDKeys can detect it).
	if n := len(ParseAuthorizedKeys("<!doctype html>\n<html><body>nope</body></html>")); n != 0 {
		t.Errorf("HTML page parsed as %d keys, want 0", n)
	}
}

func TestField(t *testing.T) {
	block := "status: running\nextended_status: running\ndetail: Running module package-update-upgrade\n"
	if got := field(block, "status:"); got != "running" {
		t.Errorf("status = %q, want running", got)
	}
	if got := field(block, "detail:"); got != "Running module package-update-upgrade" {
		t.Errorf("detail = %q", got)
	}
	if got := field(block, "missing:"); got != "" {
		t.Errorf("missing key should be empty, got %q", got)
	}
}

func TestParseChecks(t *testing.T) {
	names := map[string]string{"claude": "Claude Code", "folder": "project folder"}
	out := "OK:claude\nNO:codex\nOK:folder\n"
	checks := parseChecks(out, names)
	if len(checks) != 3 {
		t.Fatalf("got %d checks, want 3", len(checks))
	}
	want := []Check{
		{Name: "Claude Code installed", OK: true},
		{Name: "codex not found", OK: false}, // key not in names -> falls back to the key
		{Name: "project folder present", OK: true},
	}
	for i, w := range want {
		if checks[i] != w {
			t.Errorf("check[%d] = %+v, want %+v", i, checks[i], w)
		}
	}
}
