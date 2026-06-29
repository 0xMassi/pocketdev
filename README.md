# pocketdev

Run the AI coding CLI you already pay for on a cloud box you reach from your phone.

`pocketdev` provisions an always-on Hetzner server, locks it to your private Tailscale network (no public SSH), and installs the agent you use: Claude Code, Codex, Cursor, opencode, Gemini, Grok, or Aider. You log in with your own subscription. Then you SSH in from a laptop or from Termius on a phone and code from anywhere.

Two commands and you are done:

```
# on your laptop
pocketdev

# on the box (once)
ssh dev@devbox
pocketdev setup
```

## How it works

`pocketdev` (laptop) walks one step at a time: paste a Hetzner token (it checks the token before moving on), choose the box, connect Tailscale, choose which agents to install on the box, and pick what code the box starts with. Each console page opens only when you reach that step and say so.

The **Project** step picks one source for the box's code:

- **Clone a GitHub repo.** Pick from a searchable list of your repos, read live via the local `gh` CLI; pocketdev clones it on the box during `pocketdev setup` (after `gh auth login`). If `gh` isn't installed locally, type `owner/repo` instead.
- **Copy a local folder.** Browse your filesystem (start typing to filter, `ctrl+a` toggles hidden files), pick a folder or file, and pocketdev rsyncs it to the box over the tailnet once the box is up.
- **Start fresh.** An empty box.

The box step offers two paths:

- **Create a new box.** Drill down Hetzner's hierarchy, the same as the console: Type (Shared / Dedicated) → Class (Cost-Optimized, Regular Performance, or General Purpose) → Architecture (x86 / Arm64), with any single-choice step skipped. Then a table (headers, full region names with country flags, live net monthly prices ex VAT in your account currency) where each row is a concrete size + region, cheapest first. Type to filter, `shift+tab` to step back. pocketdev creates a firewall that drops all public inbound, boots Ubuntu 24.04 with that firewall attached, joins it to your tailnet, and installs the agent CLIs. Nothing listens on the public internet.
- **Adopt a server you already own.** Pick one of your existing servers; pocketdev joins it to your tailnet and installs the agents over SSH without rebuilding it. `pocketdev destroy` never deletes an adopted server (you owned it first).

`pocketdev destroy` deletes a created box and its firewall. With an OAuth client it also revokes the tailnet node; with a plain auth key, remove the node from the Tailscale console (it goes offline on its own when the box is gone).

After the box is up, pocketdev waits for first-boot to finish (cloud-init also runs `apt upgrade`), verifies over SSH that each agent CLI is installed, then offers to drop you into `pocketdev setup`.

`pocketdev setup` (on the box) installs anything missing, signs into GitHub and clones your repo, then runs each agent's login. The login runs over your SSH session, so the browser or paste-code flow works the same as it does on your laptop. (The clone runs before the agent logins, so your project is there even if you skip a login.)

After that: `ssh dev@devbox`, start tmux, run your agent.

## Install

```
go install github.com/0xMassi/pocketdev@latest
```

Or build from source:

```
git clone https://github.com/0xMassi/pocketdev
cd pocketdev && go build -o pocketdev .
```

You need `go` 1.25+ to build, and `ssh-keygen` on your laptop (`pocketdev` generates an SSH key for you if you lack one).

## What you need first

- A Hetzner Cloud account and a project API token with Read & Write permission. The TUI opens the console and tells you where to click.
- A Tailscale account and one **reusable auth key** (Settings → Keys → Generate auth key → toggle Reusable). That's the only web step: no ACL edits, no OAuth client, no MagicDNS toggle. pocketdev opens the keys page for you, and finds the box on your tailnet using your local `tailscale` CLI. *(Advanced/zero-touch: set `TS_OAUTH_CLIENT_ID` + `TS_OAUTH_CLIENT_SECRET` for a tagged, auto-revoking node; `tailscale-acl.hujson` is the policy snippet for that path.)*

You only do this once. `pocketdev` saves your config to `~/.config/pocketdev/config.json` (0600), so the next box skips the credential prompts.

## Supported agents

| Agent | CLI | Auth |
|-------|-----|------|
| Claude Code | `claude` | Claude Pro/Max (paste-code login over SSH) |
| OpenAI Codex | `codex` | ChatGPT Plus/Pro/Business (`codex login --device-auth`) |
| opencode | `opencode` | Claude Pro/Max, GitHub Copilot, ChatGPT |
| Cursor | `cursor-agent` | Cursor Pro/Business |
| Google Gemini | `gemini` | Google account, or `GEMINI_API_KEY` |
| Grok Build | `grok` | SuperGrok / X Premium+, or `GROK_CODE_XAI_API_KEY` |
| Aider | `aider` | API key only (no subscription login) |

Pick one or several. `pocketdev setup` installs each and runs its login.

## Security

- The Hetzner firewall has no inbound rules, so it drops every public packet. Tailscale starts outbound, so it still connects. Port 22 never opens to the internet.
- Access goes through hardened OpenSSH on the tailnet: key only, no root login, `AllowUsers dev`. The `dev` user has no sudo, so an agent running arbitrary shell cannot escalate to root.
- Your Hetzner token and Tailscale secret stay on your laptop. The one credential that reaches the box, the Tailscale auth key, is single-use, tagged, and expires in 30 minutes.
- `unattended-upgrades` patches the box and reboots in a quiet window (05:00 by default). A reboot ends the tmux session (processes don't survive it), but your code on disk does, and `claude --resume` reopens the last conversation, so you pick up where you left off.

Break-glass when the box is unreachable: Hetzner Rescue mode gives you a root shell and mounts the disk, so you can read `/var/log/cloud-init-output.log`.

## Cost

Hetzner CAX21 (4 vCPU / 8 GB ARM) runs about €8/mo with an IPv4, €8/mo without VAT. CAX11 (4 GB) is the cheap option at about €5/mo and works for a single light session. ARM lives in the EU regions (nbg1, fsn1, hel1); use the x86 CX32 in `ash` or `hil` for the US. Tailscale is free for personal use up to 6 users. A powered-off Hetzner server still bills, so `pocketdev destroy` is the way to stop charges.

## Code from your phone

After a create, pocketdev prints a **"Code from your phone"** card with a scannable QR, the host details, and these steps. Run `pocketdev mobile` anytime to show it again, authorize a phone key, and write an `~/.ssh/config` entry (so `ssh devbox` works in any terminal).

The goal: open your SSH client, tap the box, FaceID, you're attached to the live tmux `code` session with your agent.

### The key is the catch

The box runs **key-only** SSH and trusts only keys it already knows, which at first is your laptop's alone. A QR can't carry a private key: it holds the *address* (`ssh://dev@<host>`) and nothing secret, and Termius free has no cloud key sync. Scan the QR with no key on the phone and you get "connection refused." So put a trusted key on the phone once. Easiest first:

- **Termius SSH ID (recommended, no key files).** Sign in to Termius and enable **SSH ID**: each device generates its own key (on a passkey-capable device, a non-exportable, FaceID-bound `sk-` key) and publishes only the **public** halves at `sshid.io/<your-handle>`. Run `pocketdev mobile`, choose *Termius SSH ID*, and enter your handle; pocketdev fetches every device's public key over `https`, validates each, and appends them to the box's `authorized_keys` in one shot. No key file leaves a device, your hardened key-only SSH and Mosh stay untouched, and one handle covers all your devices. `pocketdev mobile --sshid <handle>` skips the prompt.
- **Phone generates a key, you paste it.** Termius → **Keychain → New Key → generate (ed25519)** → copy the **public** half → `pocketdev mobile` → *Paste a public key*. The same trust model as SSH ID, one device at a time.
- **Reuse the laptop's key (simplest on Apple).** AirDrop `~/.ssh/id_ed25519` to the phone and import it in Termius (**Keychain → Import**). Nothing to authorize, since it's already trusted, though laptop and phone then share one key.

### One-time on the phone

1. Install the **Tailscale app**, sign into the *same* account, and turn on **auto-reconnect** (iOS: VPN On Demand). This keeps the tunnel up in the background so later taps connect without a prompt. (Tailscale has no URL scheme, so this step can't be scripted.)
2. Get a key onto the phone (see *The key is the catch* above; SSH ID is the no-files path).
3. Install an SSH/Mosh client and add the host:
   - **Termius (free).** Install from **termius.com**, *not* the Mac App Store (the App Store build can't import keys). New Host with the card's fields (address = the MagicDNS FQDN, or the `100.x` IP as a fallback; user `dev`), attach your key, enable **Mosh** (yes, it's on the free Starter plan), set the startup command `tmux new -As code`. Then it's open → tap → FaceID → coding. *(Host sync across your devices needs Pro; SSH ID public keys sync on free.)*
   - **Blink Shell (paid, true one-tap).** Once a key is configured, scan the card's `ssh://dev@<fqdn>` QR, or wrap a `blinkshell://run` URL in an iOS Shortcut and "Add to Home Screen" for a literal one-tap-from-home-screen.

> Tried Tailscale SSH (no key at all)? It works, but on this hardened, tailnet-only box it's a step down: the default tailnet policy runs it in "check" mode (a browser re-auth every ~12h, and it breaks Blink), reaching seamless mode needs a one-time ACL edit, it bypasses the box's key-only/no-root SSH hardening, and Mosh support is unreliable through it. SSH ID keeps the hardening, the second factor, and Mosh, for the same "tap and you're in" feel.

**Staying connected.** Mosh keeps the transport alive across lock screens and Wi-Fi↔cellular handoffs; the server-side tmux `code` session keeps your agent running when the app is killed, and you can reattach the *same* session from your phone or laptop. Reconnecting drops you back where you were. A reboot is the one thing tmux doesn't survive, but your files do, and `claude --resume` restores the conversation. Pin Esc/Ctrl/Tab/arrows to the client's shortcut bar for the TUI.

> Notes: the MagicDNS name only resolves while Tailscale is connected (use the `100.x` IP if a connect fails to resolve). Running any other full-tunnel VPN evicts Tailscale (iOS allows one VPN) and the box goes unreachable. Prefer the per-device key model; don't sync a box private key into a third-party cloud vault.

## Publish to the web

Tailscale lets *you* reach the box. To let *anyone* reach something you built on it (a dev server, a demo, a live site), use `pocketdev publish` on the box. It opens an outbound Cloudflare tunnel, so a public `https://` URL works while the firewall stays empty and no inbound port ever opens.

```
# on the box, in a tmux window
pocketdev publish 3000
```

This installs `cloudflared` on first use (a static binary in `~/.local/bin`, no root) and prints a `https://<random>.trycloudflare.com` URL pointing at `localhost:3000`. Zero account, zero domain, instant. The URL is ephemeral and meant for demos and sharing in progress.

For a persistent URL on your own domain, create a tunnel in the Cloudflare Zero Trust dashboard (public hostname → `http://localhost:3000`), copy its token, and run:

```
pocketdev publish --token <token>
```

Run either in its own tmux window so your app keeps serving while you work. Stop it with Ctrl-C.

## Teardown

```
pocketdev destroy
```

This deletes the server and firewall and revokes the tailnet node.

## Status

Early. The personal flow works end to end. A multi-user product runs into one constraint: a Claude Pro/Max (or any consumer) subscription is per-seat and can't back paying customers, so a hosted version has to be bring-your-own-auth, defaulting to a customer-supplied API key or a Claude for Work seat. pocketdev sells the box, the security, and the mobile workflow, never the model access.

## License

[AGPL-3.0](LICENSE). Self-host, modify, and use pocketdev as you like. If you
run a modified version as a network service, the AGPL requires you to publish your
source. For a commercial or closed-source arrangement, contact the author.
