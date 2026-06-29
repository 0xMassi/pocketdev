# Security Policy

pocketdev provisions servers and touches credentials, so security reports matter.

## Reporting a vulnerability

Report security issues privately through GitHub's
[private vulnerability reporting](https://github.com/0xMassi/pocketdev/security/advisories/new)
(the repo's **Security** tab → **Report a vulnerability**). Don't open a public
issue for a vulnerability.

Expect a first response within a few days. When a fix is ready, the release and
the advisory go out together.

## Supported versions

pocketdev is pre-1.0. Fixes land on the latest tagged release; upgrade before
reporting.

## What pocketdev does to stay safe

- The Hetzner firewall has no inbound rules, so it drops every public packet.
  Access runs over Tailscale only; port 22 never opens to the internet.
- OpenSSH is hardened: key-only, no root login, `AllowUsers dev`. The `dev` user
  has no sudo, so an agent running shell can't escalate to root.
- Your Hetzner token and Tailscale secret stay on your machine. The one
  credential that reaches the box, a Tailscale auth key, is short-lived.
- Config is written `0600` to your user config dir, never to the repo.
- A pasted SSH public key or an SSH ID handle is validated and rejected if it
  carries shell metacharacters before it reaches a remote command.

## Your side

- Keep your Hetzner and Tailscale accounts behind MFA.
- Don't sync a box's private key into a third-party cloud vault.
- Run `pocketdev destroy` when you're done to stop billing and shrink the
  footprint.
