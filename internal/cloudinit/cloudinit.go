// Package cloudinit renders the first-boot cloud-config for the box: a hardened,
// Tailscale-only host that installs the chosen agent CLIs and an on-box `pocketdev`
// helper. The YAML is built from typed values and marshalled, so there is no
// fragile string templating.
package cloudinit

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/0xMassi/pocketdev/internal/agents"
	"gopkg.in/yaml.v3"
)

// Params are the inputs deploy-time values come from.
type Params struct {
	Hostname   string
	Tag        string
	TSAuthKey  string
	RebootTime string
	SSHKeys    []string
	Agents     []agents.Agent
	GitHub     bool   // install gh and offer `gh auth login` in `pocketdev setup`
	RepoURL    string // optional repo to clone on the box
	AdoptUser  string // for RenderBootstrap: the login user that runs `pocketdev install`
}

type writeFile struct {
	Path        string `yaml:"path"`
	Permissions string `yaml:"permissions,omitempty"`
	Encoding    string `yaml:"encoding,omitempty"`
	Content     string `yaml:"content"`
}

type cloudConfig struct {
	PackageUpdate  bool              `yaml:"package_update"`
	PackageUpgrade bool              `yaml:"package_upgrade"`
	Locale         string            `yaml:"locale"`
	Packages       []string          `yaml:"packages"`
	Users          []map[string]any  `yaml:"users"`
	Swap           map[string]string `yaml:"swap"`
	WriteFiles     []writeFile       `yaml:"write_files"`
	Runcmd         []string          `yaml:"runcmd"`
	FinalMessage   string            `yaml:"final_message"`
}

var basePackages = []string{
	"git", "curl", "jq", "tmux", "mosh", "ufw",
	"build-essential", "python3", "ca-certificates", "gnupg", "unattended-upgrades",
}

const sshdHarden = `PasswordAuthentication no
PermitRootLogin no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
X11Forwarding no
AllowUsers dev
`

const autoUpgrades = `APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
`

const profile = `export PATH="$HOME/.local/bin:$HOME/.opencode/bin:$HOME/.npm-global/bin:$HOME/.grok/bin:$PATH"
export BROWSER=/usr/local/bin/pocketdev-open
alias code='tmux new -As code'
`

// pocketdevOpen relays a URL to the laptop's browser via the reverse-forwarded
// port that `pocketdev` sets up during `Connect`; it always prints the URL too,
// so it degrades gracefully when there is no tunnel (a plain SSH session).
const pocketdevOpen = `#!/usr/bin/env bash
url="${1:-}"
[ -n "$url" ] || exit 0
curl -fsS --max-time 5 --data-urlencode "url=$url" "http://127.0.0.1:${POCKETDEV_OPEN_PORT:-47654}/open" >/dev/null 2>&1
printf '\n  -> open on your laptop: %s\n\n' "$url"
exit 0
`

// Render returns the complete "#cloud-config" document.
func Render(p Params) (string, error) {
	user := map[string]any{
		"name":                "dev",
		"shell":               "/bin/bash",
		"groups":              []string{}, // no sudo: an agent runs arbitrary shell, must not self-escalate
		"lock_passwd":         true,
		"ssh_authorized_keys": p.SSHKeys,
	}

	rebootConf := fmt.Sprintf("Unattended-Upgrade::Automatic-Reboot \"true\";\nUnattended-Upgrade::Automatic-Reboot-Time \"%s\";\n", p.RebootTime)

	cc := cloudConfig{
		PackageUpdate:  true,
		PackageUpgrade: false,         // upgrade happens in runcmd AFTER tailnet+ssh, so it's visible
		Locale:         "en_US.UTF-8", // mosh needs a UTF-8 locale
		Packages:       basePackages,
		Users:          []map[string]any{user},
		Swap:           map[string]string{"filename": "/swapfile", "size": "4G"},
		WriteFiles: []writeFile{
			{Path: "/etc/sysctl.d/99-swap.conf", Content: "vm.swappiness=10\n"},
			{Path: "/etc/ssh/sshd_config.d/99-harden.conf", Content: sshdHarden},
			{Path: "/etc/apt/apt.conf.d/20auto-upgrades", Content: autoUpgrades},
			{Path: "/etc/apt/apt.conf.d/52unattended-reboot", Content: rebootConf},
			{Path: "/etc/profile.d/pocketdev.sh", Permissions: "0644", Content: profile},
			{Path: "/usr/local/bin/pocketdev-open", Permissions: "0755", Content: pocketdevOpen},
			{Path: "/etc/pocketdev/agents", Content: agentKeys(p.Agents) + "\n"},
			{Path: "/opt/pocketdev/sys-setup.sh", Permissions: "0755", Encoding: "b64", Content: b64(sysSetup(p.Agents, p.GitHub))},
			{Path: "/usr/local/bin/pocketdev", Permissions: "0755", Encoding: "b64", Content: b64(helper(p))},
		},
		Runcmd: []string{
			"chown -R dev:dev /home/dev",
			// Make the boot log readable so `dev` can stream live progress (no
			// secrets in stdout; the authkey is never echoed).
			"chmod a+r /var/log/cloud-init-output.log 2>/dev/null || true",
			"sysctl --system",
			"install -m 0755 -d /usr/share/keyrings",
			"curl -fsSL https://pkgs.tailscale.com/stable/ubuntu/noble.noarmor.gpg -o /usr/share/keyrings/tailscale-archive-keyring.gpg",
			"curl -fsSL https://pkgs.tailscale.com/stable/ubuntu/noble.tailscale-keyring.list -o /etc/apt/sources.list.d/tailscale.list",
			"apt-get update && apt-get install -y tailscale",
			tailscaleUp(p),
			"ufw default deny incoming && ufw default allow outgoing && ufw allow in on tailscale0 to any port 22 proto tcp && ufw allow in on tailscale0 to any port 60000:61000 proto udp && ufw --force enable",
			"systemctl restart ssh",
			"systemctl enable --now unattended-upgrades",
			// Box is now on the tailnet and reachable; upgrade with progress visible.
			"DEBIAN_FRONTEND=noninteractive apt-get update && DEBIAN_FRONTEND=noninteractive apt-get -y upgrade",
			"bash /opt/pocketdev/sys-setup.sh",
			"runuser -l dev -c 'pocketdev install'",
		},
		FinalMessage: "pocketdev box ready. SSH in as dev, then run: pocketdev setup",
	}

	out, err := yaml.Marshal(cc)
	if err != nil {
		return "", err
	}
	return "#cloud-config\n" + string(out), nil
}

// RenderBootstrap produces a root shell script for ADOPTING an existing server
// over SSH: it joins the tailnet, drops the same on-box files a created box gets
// (/usr/local/bin/pocketdev, /etc/pocketdev/agents, sys-setup.sh), installs the
// system deps and the agents. It reuses the create-path generators byte-for-byte.
// It deliberately omits the ufw lockdown (don't risk locking yourself out of a
// public-IP SSH session before the tailnet path is confirmed).
func RenderBootstrap(p Params) string {
	user := p.AdoptUser
	if user == "" {
		user = "root"
	}
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\nset -euo pipefail\nexport DEBIAN_FRONTEND=noninteractive\n\n")
	b.WriteString("command -v apt-get >/dev/null 2>&1 || { echo 'pocketdev adopt supports Debian/Ubuntu hosts only'; exit 1; }\n\n")
	b.WriteString("if ! command -v tailscale >/dev/null 2>&1; then curl -fsSL https://tailscale.com/install.sh | sh; fi\n")
	b.WriteString(tailscaleUp(p) + "\n\n")
	b.WriteString("install -d -m 0755 /etc/pocketdev /opt/pocketdev\n")
	fmt.Fprintf(&b, "cat > /etc/pocketdev/agents <<'PDEOF'\n%s\nPDEOF\n", agentKeys(p.Agents))
	writeB64(&b, "/usr/local/bin/pocketdev", "0755", helper(p))
	writeB64(&b, "/opt/pocketdev/sys-setup.sh", "0755", sysSetup(p.Agents, p.GitHub))
	b.WriteString("bash /opt/pocketdev/sys-setup.sh\n")
	fmt.Fprintf(&b, "runuser -l %s -c 'pocketdev install' || echo 'pocketdev: agent install had failures'\n", user)
	b.WriteString("echo; echo 'pocketdev: adoption complete. Run: pocketdev setup'\n")
	return b.String()
}

// writeB64 emits a base64-decode heredoc so opaque content reaches disk intact.
func writeB64(b *strings.Builder, path, mode, content string) {
	fmt.Fprintf(b, "base64 -d > %s <<'PDB64'\n%s\nPDB64\nchmod %s %s\n", path, b64(content), mode, path)
}

func agentKeys(ag []agents.Agent) string {
	ks := make([]string, len(ag))
	for i, a := range ag {
		ks[i] = a.Key
	}
	return strings.Join(ks, "\n")
}

// sysSetup is the root script installing apt-level deps the chosen agents need,
// plus the GitHub CLI when requested.
func sysSetup(ag []agents.Agent, github bool) string {
	need := map[string]bool{}
	for _, a := range ag {
		for _, d := range a.SysDeps {
			need[d] = true
		}
	}
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\nset -e\nexport DEBIAN_FRONTEND=noninteractive\n")
	if need["node"] {
		b.WriteString("# Node.js 22 LTS (signed NodeSource repo)\n")
		b.WriteString("curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key | gpg --dearmor -o /usr/share/keyrings/nodesource.gpg\n")
		b.WriteString("echo 'deb [signed-by=/usr/share/keyrings/nodesource.gpg] https://deb.nodesource.com/node_22.x nodistro main' > /etc/apt/sources.list.d/nodesource.list\n")
		b.WriteString("apt-get update && apt-get install -y nodejs\n")
	}
	if need["pipx"] {
		b.WriteString("apt-get install -y pipx\n")
	}
	if need["keyring"] {
		b.WriteString("apt-get install -y gnome-keyring libsecret-1-0 libsecret-tools dbus-x11\n")
	}
	if github {
		b.WriteString("# GitHub CLI (signed apt repo)\n")
		b.WriteString("curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg -o /usr/share/keyrings/githubcli-archive-keyring.gpg\n")
		b.WriteString("chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg\n")
		b.WriteString("echo \"deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main\" > /etc/apt/sources.list.d/github-cli.list\n")
		b.WriteString("apt-get update && apt-get install -y gh\n")
	}
	return b.String()
}

// helper generates the on-box `pocketdev` command from the selected agents and
// optional GitHub/repo setup.
func helper(p Params) string {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\nset -euo pipefail\n")
	b.WriteString(`export PATH="$HOME/.local/bin:$HOME/.opencode/bin:$HOME/.npm-global/bin:$HOME/.grok/bin:$PATH"` + "\n\n")

	keys := make([]string, len(p.Agents))
	for i, a := range p.Agents {
		keys[i] = a.Key
		fmt.Fprintf(&b, "install_%s(){ command -v %s >/dev/null 2>&1 || { %s; }; }\n", a.Key, a.Bins[0], a.Install)
		fmt.Fprintf(&b, "login_%s(){ printf '\\n== %%s ==\\n' %q; echo \"hint: %s\"; %s; }\n\n", a.Key, a.Name, a.LoginHint, a.LoginCmd)
	}

	fmt.Fprintf(&b, "AGENTS=( %s )\n", strings.Join(keys, " "))
	github := "0"
	if p.GitHub {
		github = "1"
	}
	fmt.Fprintf(&b, "PD_GITHUB=%s\n", github)
	fmt.Fprintf(&b, "PD_REPO=%q\n\n", p.RepoURL)

	// One agent failing (e.g. an unreliable third-party installer) must not abort
	// install/login of the rest, even under `set -e`.
	b.WriteString(`cmd_install(){ for a in "${AGENTS[@]}"; do "install_$a" || echo "pocketdev: install_$a failed, continuing" >&2; done; }
cmd_login(){ for a in "${AGENTS[@]}"; do "login_$a" || echo "pocketdev: login_$a failed, continuing" >&2; done; }
cmd_github(){
  [ "$PD_GITHUB" = "1" ] || return 0
  command -v gh >/dev/null 2>&1 || { echo "pocketdev: gh not installed"; return 0; }
  if gh auth status >/dev/null 2>&1; then echo "GitHub: already signed in"; else printf '\n== GitHub ==\n'; gh auth login || echo "pocketdev: gh auth login skipped"; fi
}
cmd_clone(){
  [ -n "$PD_REPO" ] || return 0
  local dir="$HOME/$(basename "${PD_REPO%.git}")"
  if [ -d "$dir/.git" ]; then echo "repo already present: $dir"; return 0; fi
  printf '\n== Cloning %s ==\n' "$PD_REPO"
  if gh repo clone "$PD_REPO" "$dir" || git clone "$PD_REPO" "$dir"; then
    echo "project: $dir"
  else
    echo "pocketdev: clone failed. Run 'pocketdev github' to sign in, then 'pocketdev clone'." >&2
  fi
}
# Order matters: clone the project (after gh auth) BEFORE the interactive agent
# logins, so a project is present even if you skip a login.
cmd_setup(){ cmd_install; cmd_github; cmd_clone; cmd_login; echo; echo "Ready. Start coding: tmux new -As code"; }
cmd_status(){ echo "tailnet:"; tailscale status 2>/dev/null || true; echo "agents: ${AGENTS[*]}"; }
# cloudflared rides an OUTBOUND tunnel to Cloudflare, so it publishes a public URL
# with the deny-all firewall still shut. The static binary installs into the
# no-sudo dev user's ~/.local/bin, so publishing never needs root.
ensure_cloudflared(){
  command -v cloudflared >/dev/null 2>&1 && return 0
  PATH="$HOME/.local/bin:$PATH"
  command -v cloudflared >/dev/null 2>&1 && return 0
  echo "== Installing cloudflared (first publish) =="
  case "$(dpkg --print-architecture 2>/dev/null || uname -m)" in
    arm64|aarch64) a=arm64;;
    amd64|x86_64)  a=amd64;;
    *) echo "pocketdev: unsupported architecture for cloudflared" >&2; return 1;;
  esac
  url="https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-$a"
  bin="$HOME/.local/bin/cloudflared"; mkdir -p "$HOME/.local/bin"
  if curl -fsSL "$url" -o "$bin" 2>/dev/null || wget -qO "$bin" "$url"; then chmod +x "$bin"; else
    echo "pocketdev: could not download cloudflared" >&2; return 1; fi
}
cmd_publish(){
  ensure_cloudflared || return 1
  if [ "$1" = "--token" ] && [ -n "$2" ]; then
    echo "== Publishing via your named Cloudflare tunnel (Ctrl-C to stop) =="
    exec cloudflared tunnel run --token "$2"
  fi
  port="${1:-3000}"
  printf '\n== Publishing http://localhost:%s to the public web ==\n' "$port"
  echo "   cloudflared prints your https://… URL below. Ctrl-C stops it."
  echo "   Tip: run this in its own tmux window so your app keeps serving."
  exec cloudflared tunnel --url "http://localhost:$port" --no-autoupdate
}

case "${1:-setup}" in
  setup)   cmd_setup;;
  install) cmd_install;;
  login)   cmd_login;;
  github)  cmd_github;;
  clone)   cmd_clone;;
  status)  cmd_status;;
  publish) shift; cmd_publish "$@";;
  code)    exec tmux new -As code;;
  *) echo "usage: pocketdev {setup|login|install|github|clone|status|publish [port]|code}";;
esac
`)
	return b.String()
}

func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// tailscaleUp builds the join command, advertising a tag only when one is set
// (a plain reusable auth key is not authorized to advertise tags).
func tailscaleUp(p Params) string {
	if p.Tag != "" {
		return fmt.Sprintf("tailscale up --authkey %q --hostname %q --advertise-tags %q --accept-dns", p.TSAuthKey, p.Hostname, p.Tag)
	}
	return fmt.Sprintf("tailscale up --authkey %q --hostname %q --accept-dns", p.TSAuthKey, p.Hostname)
}
