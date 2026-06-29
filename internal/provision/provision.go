// Package provision orchestrates a deploy as discrete steps so the TUI can show
// progress: validate credentials, mint a Tailscale key, render cloud-init, create
// the server, and wait for it to join the tailnet.
package provision

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/0xMassi/pocketdev/internal/agents"
	"github.com/0xMassi/pocketdev/internal/cloudinit"
	"github.com/0xMassi/pocketdev/internal/config"
	"github.com/0xMassi/pocketdev/internal/hetzner"
	"github.com/0xMassi/pocketdev/internal/tailscale"
)

// Provisioner carries state across the deploy steps.
type Provisioner struct {
	cfg      config.Config
	agents   []agents.Agent
	hz       *hetzner.Client
	tsAccess string // Tailscale API token (OAuth path only)
	tsKey    string
	tsTag    string // advertised tag (OAuth path only; empty for a plain auth key)
	userData string

	// tsIP/tsName (the box's detected tailnet IP and label) are read by View on
	// the render goroutine and written by ResolveHost on a Cmd goroutine, so they
	// are guarded by mu.
	mu     sync.RWMutex
	tsIP   string
	tsName string
}

func (p *Provisioner) setNode(ip, name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ip != "" {
		p.tsIP = ip
	}
	if name != "" {
		p.tsName = name
	}
}

func (p *Provisioner) node() (ip, name string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.tsIP, p.tsName
}

// sshArgs prepends the standard options. For these disposable tailnet boxes we
// don't persist/validate host keys: a recreated box reuses the name/IP with a new
// host key, and accept-new would hard-fail on the stale known_hosts entry. The
// connection is already authenticated and encrypted by Tailscale (WireGuard).
func sshArgs(extra ...string) []string {
	base := []string{
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "LogLevel=ERROR",
	}
	return append(base, extra...)
}

// New validates the config shape and builds the clients.
func New(cfg config.Config) (*Provisioner, error) {
	if cfg.HCloudToken == "" {
		return nil, fmt.Errorf("missing Hetzner token")
	}
	if !cfg.UseExisting && len(cfg.SSHPublicKeys) == 0 {
		return nil, fmt.Errorf("no SSH public key configured")
	}
	if cfg.TSAuthKey == "" && (cfg.TSOAuthID == "" || cfg.TSOAuthSecret == "") {
		return nil, fmt.Errorf("provide a Tailscale OAuth client (id + secret) or a raw auth key")
	}
	ag, err := agents.Resolve(cfg.Agents)
	if err != nil {
		return nil, err
	}
	if len(ag) == 0 {
		return nil, fmt.Errorf("select at least one agent")
	}
	return &Provisioner{cfg: cfg, agents: ag, hz: hetzner.New(cfg.HCloudToken)}, nil
}

// ValidateCredentials probes Hetzner and (if used) the Tailscale OAuth client.
func (p *Provisioner) ValidateCredentials(ctx context.Context) error {
	if err := p.hz.Validate(ctx); err != nil {
		return fmt.Errorf("hetzner token: %w", err)
	}
	if p.cfg.TSAuthKey == "" {
		if err := tailscale.Validate(ctx, p.cfg.TSOAuthID, p.cfg.TSOAuthSecret); err != nil {
			return err
		}
	}
	return nil
}

// PrepareTailscale mints a short-lived tagged key (OAuth path) and revokes any
// stale node with the same hostname so the new box keeps the clean MagicDNS name.
func (p *Provisioner) PrepareTailscale(ctx context.Context) error {
	if p.cfg.TSAuthKey != "" {
		p.tsKey = p.cfg.TSAuthKey // plain key: no tag, no API access
		return nil
	}
	key, access, err := tailscale.MintKey(ctx, p.cfg.TSOAuthID, p.cfg.TSOAuthSecret, p.cfg.TSTag)
	if err != nil {
		return err
	}
	p.tsKey, p.tsAccess, p.tsTag = key, access, p.cfg.TSTag
	return tailscale.RevokeByHostname(ctx, access, p.cfg.Name)
}

// Render builds the cloud-init document.
func (p *Provisioner) Render() error {
	ud, err := cloudinit.Render(cloudinit.Params{
		Hostname:   p.cfg.Name,
		Tag:        p.tsTag,
		TSAuthKey:  p.tsKey,
		RebootTime: p.cfg.RebootTime,
		SSHKeys:    p.cfg.SSHPublicKeys,
		Agents:     p.agents,
		GitHub:     p.cfg.GitHub,
		RepoURL:    p.cfg.RepoURL,
	})
	if err != nil {
		return err
	}
	p.userData = ud
	return nil
}

// CreateServer creates the box (idempotent: skips if the name already exists).
func (p *Provisioner) CreateServer(ctx context.Context) error {
	exists, err := p.hz.Exists(ctx, p.cfg.Name)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("a server named %q already exists (use `pocketdev destroy` first)", p.cfg.Name)
	}
	_, err = p.hz.Create(ctx, hetzner.CreateOpts{
		Name:        p.cfg.Name,
		Region:      p.cfg.Region,
		ServerType:  p.cfg.ServerType,
		WithoutIPv4: p.cfg.WithoutIPv4,
		UserData:    p.userData,
		PublicKeys:  p.cfg.SSHPublicKeys,
	})
	return err
}

// WaitForTailnet blocks until the box joins the tailnet. With an OAuth token it
// polls the Tailscale API; otherwise it watches the local `tailscale` CLI. Local
// detection is best-effort: if the CLI is absent or the node hasn't appeared, it
// returns ("", nil) rather than failing a deploy that is otherwise fine.
func (p *Provisioner) WaitForTailnet(ctx context.Context) (string, error) {
	if p.tsAccess != "" {
		ip, err := tailscale.WaitForNode(ctx, p.tsAccess, p.cfg.Name, 30, 10*time.Second)
		if err != nil {
			return "", err
		}
		p.setNode(ip, p.cfg.Name)
		return ip, nil
	}
	ip, host := waitLocalNode(ctx, p.cfg.Name, 30, 8*time.Second)
	p.setNode(ip, host)
	return ip, nil
}

// ResolveHost refreshes the box's online tailnet IP/name from the local CLI.
// Best-effort; used between boot-progress polls so we always target the live node.
func (p *Provisioner) ResolveHost(ctx context.Context) {
	if bin := tailscaleBin(); bin != "" {
		if ip, host := detectNode(ctx, bin, p.cfg.Name); ip != "" {
			p.setNode(ip, host)
		}
	}
}

// Node returns the box's display name (tailnet label, possibly name-N) and IP.
func (p *Provisioner) Node() (name, ip string) {
	ip, name = p.node()
	if name == "" {
		name = p.cfg.Name
	}
	return name, ip
}

// Agents returns the resolved agent set (for the result screen).
func (p *Provisioner) Agents() []agents.Agent { return p.agents }

// SSHUserHost returns the login user and the best host (tailnet IP, else name).
func (p *Provisioner) SSHUserHost() (user, host string) {
	user = "dev"
	if p.cfg.UseExisting {
		user = p.cfg.SSHUser
	}
	host, _ = p.node()
	if host == "" {
		host = p.cfg.Name
	}
	return user, host
}

func (p *Provisioner) sshRun(ctx context.Context, remote string) (string, error) {
	user, host := p.SSHUserHost()
	args := sshArgs("-o", "ConnectTimeout=10", "-o", "BatchMode=yes", user+"@"+host, remote)
	out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	return string(out), err
}

// AuthorizeKey appends one pubkey to the box's authorized_keys over SSH, so a
// second device (your phone) can connect using a key it generated itself — the
// private half never leaves that device. The laptop's own (already-trusted) key
// is what logs in to do this. Idempotent: re-adding the same key is a no-op.
func AuthorizeKey(ctx context.Context, user, host, pubkey string) error {
	if !ValidPubKey(strings.TrimSpace(pubkey)) {
		return fmt.Errorf("that doesn't look like an SSH public key (expected e.g. `ssh-ed25519 AAAA... you@phone`)")
	}
	_, err := AuthorizeKeys(ctx, user, host, []string{pubkey})
	return err
}

// AuthorizeKeys appends every valid public key in keys to the box's
// authorized_keys in a single SSH round-trip, idempotently, and reports how many
// were newly added (already-present keys are skipped). Invalid lines are dropped.
func AuthorizeKeys(ctx context.Context, user, host string, keys []string) (added int, err error) {
	var valid []string
	for _, k := range keys {
		if k = strings.TrimSpace(k); ValidPubKey(k) {
			valid = append(valid, k)
		}
	}
	if len(valid) == 0 {
		return 0, fmt.Errorf("no valid public keys to authorize")
	}
	// Each key is safe to single-quote: ValidPubKey rejects quotes/newlines/
	// backslashes, so it can't escape the quoted context in the remote shell.
	var b strings.Builder
	b.WriteString(`set -e; f="$HOME/.ssh/authorized_keys"; mkdir -p "$HOME/.ssh"; chmod 700 "$HOME/.ssh"; touch "$f"; chmod 600 "$f"; n=0; `)
	for _, k := range valid {
		fmt.Fprintf(&b, "grep -qxF '%s' \"$f\" || { printf '%%s\\n' '%s' >> \"$f\"; n=$((n+1)); }; ", k, k)
	}
	b.WriteString(`echo "PD_ADDED=$n"`)
	args := sshArgs("-o", "ConnectTimeout=15", "-o", "BatchMode=yes", user+"@"+host, b.String())
	out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	if v := field(string(out), "PD_ADDED="); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &added)
	}
	return added, nil
}

// FetchSSHIDKeys pulls the public keys published for a Termius SSH ID handle from
// https://sshid.io/<handle> (the same list `curl` returns). Each device's private
// key stays on that device; only these public halves are ever fetched. Every line
// is validated before it can be installed.
func FetchSSHIDKeys(ctx context.Context, handle string) ([]string, error) {
	handle = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(handle), "@"))
	if !ValidHandle(handle) {
		return nil, fmt.Errorf("invalid SSH ID handle %q (use your sshid.io name: letters, digits, . _ -)", handle)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://sshid.io/"+handle, nil)
	if err != nil {
		return nil, err
	}
	// sshid.io serves the HTML page to browsers and raw keys to plain clients —
	// ask for text/plain so we get the authorized_keys lines, not markup.
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("User-Agent", "pocketdev")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sshid.io returned %s for @%s", resp.Status, handle)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, err
	}
	keys := ParseAuthorizedKeys(string(body))
	if len(keys) == 0 {
		if strings.Contains(strings.ToLower(string(body)), "<!doctype") {
			return nil, fmt.Errorf("sshid.io returned a web page, not keys — is @%s a real SSH ID handle?", handle)
		}
		return nil, fmt.Errorf("no public keys published for @%s — add a device in Termius SSH ID first", handle)
	}
	return keys, nil
}

// ParseAuthorizedKeys keeps only the lines of body that are valid SSH public keys.
func ParseAuthorizedKeys(body string) []string {
	var keys []string
	for line := range strings.SplitSeq(body, "\n") {
		if line = strings.TrimSpace(line); ValidPubKey(line) {
			keys = append(keys, line)
		}
	}
	return keys
}

// ValidHandle checks an SSH ID handle is safe to put in a URL path.
func ValidHandle(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// ValidPubKey does a cheap structural check on an SSH public key line and, since
// the value is interpolated into a remote shell command, rejects shell/line
// metacharacters so it can never break out of the single-quoted context.
func ValidPubKey(s string) bool {
	if s == "" || strings.ContainsAny(s, "'\"\n\r\\") {
		return false
	}
	f := strings.Fields(s)
	if len(f) < 2 || len(f[1]) < 16 {
		return false
	}
	switch f[0] {
	case "ssh-ed25519", "ssh-rsa", "ssh-dss",
		"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
		"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com":
		return true
	}
	return false
}

// BootProgress returns one snapshot of cloud-init's state: status is
// "done"/"running"/"error"/"" and detail is the latest activity (the last
// output-log line if readable, else cloud-init's own detail field). err is set
// only when the box can't be reached yet (e.g. sshd not up) — callers keep polling.
func (p *Provisioner) BootProgress(ctx context.Context) (status, detail string, err error) {
	// `|| true` so a non-zero exit from cloud-init status, or an unreadable
	// output log (tail), does NOT make ssh look like a failure — we parse the
	// captured output and only treat it as unreachable when nothing came back.
	out, sshErr := p.sshRun(ctx,
		"cloud-init status --long 2>/dev/null || true; echo __PD__; tail -n1 /var/log/cloud-init-output.log 2>/dev/null || true")
	block, logLine := out, ""
	if i := strings.Index(out, "__PD__"); i >= 0 {
		block, logLine = out[:i], strings.TrimSpace(out[i+len("__PD__"):])
	}
	status = field(block, "status:")
	detail = field(block, "detail:")
	if logLine != "" {
		detail = logLine
	}
	if status == "" && sshErr != nil {
		return "", firstLine(out), sshErr // genuinely couldn't reach the box
	}
	return status, detail, nil
}

func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func field(block, key string) string {
	for line := range strings.SplitSeq(block, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, key); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Check is one verification result.
type Check struct {
	Name string
	OK   bool
}

// Verify SSHes in and checks that each agent CLI is installed (and the rsync'd
// folder is present, if applicable).
func (p *Provisioner) Verify(ctx context.Context) ([]Check, error) {
	var script strings.Builder
	// A non-login SSH command doesn't source /etc/profile.d, so the agent bin
	// dirs aren't on PATH — add them, or `command -v` false-negatives.
	script.WriteString(`export PATH="$HOME/.local/bin:$HOME/.opencode/bin:$HOME/.npm-global/bin:$HOME/.grok/bin:$PATH"` + "\n")
	names := map[string]string{}
	for _, a := range p.agents {
		fmt.Fprintf(&script, "command -v %s >/dev/null 2>&1 && echo OK:%s || echo NO:%s\n", a.Bins[0], a.Key, a.Key)
		names[a.Key] = a.Name
	}
	if p.cfg.Source == "rsync" && p.cfg.LocalPath != "" {
		base := filepath.Base(strings.TrimRight(p.cfg.LocalPath, "/"))
		fmt.Fprintf(&script, "[ -e ~/%s ] && echo OK:folder || echo NO:folder\n", base)
		names["folder"] = "project folder"
	}
	out, err := p.sshRun(ctx, script.String())
	checks := parseChecks(out, names)
	// Surface a real SSH failure (host-key, refused, denied) when it produced no
	// parseable results — otherwise it would be silently lost.
	if len(checks) == 0 && err != nil {
		return nil, fmt.Errorf("verify over ssh failed: %s", firstLine(out))
	}
	return checks, nil
}

func parseChecks(out string, names map[string]string) []Check {
	var checks []Check
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		ok := strings.HasPrefix(line, "OK:")
		if !ok && !strings.HasPrefix(line, "NO:") {
			continue
		}
		key := line[3:]
		name := names[key]
		if name == "" {
			name = key
		}
		suffix := " installed"
		if key == "folder" {
			suffix = " present"
		}
		if !ok {
			suffix = " not found"
		}
		checks = append(checks, Check{Name: name + suffix, OK: ok})
	}
	return checks
}

// Connect opens an interactive SSH session that runs `pocketdev setup` on the
// box (agent logins, gh auth, clone) and returns when the user exits it. It also
// reverse-forwards a local URL-opener so the box's browser-opens (gh/claude auth)
// pop in the LAPTOP browser instead of failing on the headless box.
func (p *Provisioner) Connect(ctx context.Context) error {
	user, host := p.SSHUserHost()
	extra := []string{"-t"}

	if ln, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		lp := ln.Addr().(*net.TCPAddr).Port
		srv := &http.Server{Handler: http.HandlerFunc(openURLHandler)}
		go func() { _ = srv.Serve(ln) }()
		defer srv.Close()
		// The box's pocketdev-open script curls 127.0.0.1:47654, tunneled here.
		extra = append(extra, "-R", fmt.Sprintf("127.0.0.1:47654:127.0.0.1:%d", lp))
	}

	remote := "BROWSER=/usr/local/bin/pocketdev-open pocketdev setup"
	cmd := exec.CommandContext(ctx, "ssh", sshArgs(append(extra, user+"@"+host, remote)...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// authHosts are the domains whose URLs we'll auto-open on the laptop. Restricting
// to known auth providers stops a box from popping arbitrary pages.
var authHosts = []string{
	"github.com", "claude.ai", "anthropic.com", "chatgpt.com", "openai.com",
	"cursor.com", "x.ai", "google.com", "tailscale.com",
}

func allowedAuthURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, d := range authHosts {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

func openURLHandler(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	if raw := r.FormValue("url"); allowedAuthURL(raw) {
		openLocal(raw)
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusForbidden)
}

func openLocal(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if cmd.Start() == nil {
		go func() { _ = cmd.Wait() }()
	}
}

// SyncLocal rsyncs the chosen local folder/file to the box over the tailnet,
// after it has joined. A directory is copied into ~/<name>/; a file into ~/.
func (p *Provisioner) SyncLocal(ctx context.Context) error {
	if p.cfg.Source != "rsync" || p.cfg.LocalPath == "" {
		return nil
	}
	info, err := os.Stat(p.cfg.LocalPath)
	if err != nil {
		return fmt.Errorf("local path: %w", err)
	}
	user := "dev"
	if p.cfg.UseExisting {
		user = p.cfg.SSHUser
	}
	ip, name := p.node()
	host := ip
	if host == "" {
		host = name // detected tailnet label
	}
	if host == "" {
		host = p.cfg.Name // last-resort MagicDNS fallback
	}
	sshOpt := "ssh -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=accept-new -o LogLevel=ERROR -o ConnectTimeout=20"

	var args []string
	if info.IsDir() {
		base := filepath.Base(strings.TrimRight(p.cfg.LocalPath, "/"))
		args = []string{"-az", "--filter=:- .gitignore", "-e", sshOpt,
			p.cfg.LocalPath + "/", fmt.Sprintf("%s@%s:~/%s/", user, host, base)}
	} else {
		args = []string{"-az", "-e", sshOpt,
			p.cfg.LocalPath, fmt.Sprintf("%s@%s:~/", user, host)}
	}
	cmd := exec.CommandContext(ctx, "rsync", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		hint := ""
		if ip == "" { // reaching by name; box IP was never detected
			hint = " (ensure Tailscale is running on this laptop and the box is reachable as " + host + "; check `tailscale status`, then re-run: pocketdev)"
		}
		return fmt.Errorf("rsync to %s@%s failed%s: %w", user, host, hint, err)
	}
	return nil
}

// AdoptServer bootstraps an existing server over SSH: it pipes a generated script
// (tailscale join + on-box files + agent install) to the box. Nothing is written
// to the box's disk except the intended files; the script arrives on stdin.
func (p *Provisioner) AdoptServer(ctx context.Context) error {
	user := p.cfg.SSHUser
	if user == "" {
		user = "root"
	}
	host := p.cfg.SSHHost
	if host == "" {
		host = p.cfg.Name // fall back to the MagicDNS name
	}
	script := cloudinit.RenderBootstrap(cloudinit.Params{
		Hostname:  p.cfg.Name,
		Tag:       p.tsTag,
		TSAuthKey: p.tsKey,
		Agents:    p.agents,
		GitHub:    p.cfg.GitHub,
		RepoURL:   p.cfg.RepoURL,
		AdoptUser: user,
	})
	remote := "bash -s"
	if user != "root" {
		remote = "sudo -n bash -s" // -n: fail fast instead of hanging on a password prompt
	}
	cmd := exec.CommandContext(ctx, "ssh",
		sshArgs("-o", "ConnectTimeout=15", fmt.Sprintf("%s@%s", user, host), remote)...)
	cmd.Stdin = strings.NewReader(script)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("adopt %s@%s (needs root or passwordless sudo, and your SSH key on the box): %w", user, host, err)
	}
	if p.tsAccess != "" {
		_ = p.hz.LabelAdopted(ctx, p.cfg.Name)
	}
	return nil
}

// Destroy deletes the server + firewall and revokes the tailnet node. A server
// pocketdev adopted (rather than created) is never deleted — only its tailnet
// node is revoked — since the user owned that machine first.
func Destroy(ctx context.Context, cfg config.Config) error {
	if cfg.HCloudToken == "" {
		return fmt.Errorf("missing Hetzner token")
	}
	hz := hetzner.New(cfg.HCloudToken)
	adopted := cfg.UseExisting || hz.IsAdopted(ctx, cfg.Name)
	if adopted {
		fmt.Printf("%q was adopted, not created — leaving the server in place; only revoking its tailnet node.\n", cfg.Name)
	} else if err := hz.Destroy(ctx, cfg.Name); err != nil {
		return err
	}
	if cfg.TSAuthKey == "" && cfg.TSOAuthID != "" {
		access, err := tailscale.MintAccess(ctx, cfg.TSOAuthID, cfg.TSOAuthSecret)
		if err == nil {
			_ = tailscale.RevokeByHostname(ctx, access, cfg.Name)
		}
	}
	return nil
}
