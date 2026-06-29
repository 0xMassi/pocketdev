// Package mobile renders the "Code from your phone" card (with a scannable QR)
// and manages an idempotent ~/.ssh/config entry for the box, so connecting from
// Termius/Blink (or any terminal) is as close to one tap as the apps allow.
package mobile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	qrterminal "github.com/mdp/qrterminal/v3"
)

// Box is everything the mobile/desktop clients need to reach the server.
type Box struct {
	Name    string // alias, e.g. "devbox"
	FQDN    string // MagicDNS FQDN, e.g. "devbox.tailXXXX.ts.net" (preferred)
	IP      string // 100.x tailnet IP (fallback / robust when MagicDNS isn't resolved)
	User    string // "dev"
	KeyPath string // ~/.ssh/id_ed25519
}

// host is the address clients should use: the FQDN if known, else the IP.
func (b Box) host() string {
	if b.FQDN != "" {
		return b.FQDN
	}
	return b.IP
}

// SSHURL is the ssh:// link encoded in the QR (only our own MagicDNS name/IP — no
// untrusted input — so it can't be an injection vector in an ssh:// handler).
func (b Box) SSHURL() string {
	return "ssh://" + b.User + "@" + b.host()
}

// sshConfigBlock is the idempotent Host stanza, fenced by markers so it can be
// rewritten or removed cleanly.
func (b Box) sshConfigBlock() string {
	return fmt.Sprintf(`%s
Host %s
    HostName %s
    User %s
    IdentityFile %s
    IdentitiesOnly yes
%s
`, beginMarker(b.Name), b.Name, b.host(), b.User, b.KeyPath, endMarker(b.Name))
}

func beginMarker(name string) string { return "# >>> pocketdev " + name }
func endMarker(name string) string   { return "# <<< pocketdev " + name }

// Card renders the printable "Code from your phone" panel with the QR.
func Card(b Box) string {
	var s strings.Builder
	fmt.Fprintf(&s, "\nCode from your phone — %s\n\n", b.Name)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&s, "  %-16s %s\n", k, v)
		}
	}
	row("Host (MagicDNS)", b.FQDN)
	row("Tailscale IP", b.IP)
	row("User", b.User)
	row("Key", b.KeyPath)
	row("Mosh", "ON")
	row("Startup", "tmux new -As code")

	s.WriteString("\n  QR = the address (so you don't type it). It can't carry your key — set\n")
	s.WriteString("  that up in step 2 below. In Blink, the QR connects in one tap once keyed.\n\n")
	s.WriteString(qrBlock(b.SSHURL()))
	fmt.Fprintf(&s, "  %s\n\n", b.SSHURL())

	s.WriteString("  First time on your phone (once):\n")
	s.WriteString("   1) Tailscale app -> sign in to the SAME account -> turn ON auto-reconnect.\n")
	s.WriteString("   2) Give the box a key it trusts (it won't accept a QR alone). Easiest:\n")
	s.WriteString("      Termius -> sign in -> SSH ID (it makes a FaceID-bound key on the phone),\n")
	s.WriteString("      then run `pocketdev mobile` here and enter your SSH ID handle — we add\n")
	s.WriteString("      every device's PUBLIC key for you (the private keys never leave them).\n")
	s.WriteString("      (Manual instead: generate a key in Termius Keychain and paste its public\n")
	fmt.Fprintf(&s, "       half, or AirDrop this laptop's key %s and import it.)\n", b.KeyPath)
	s.WriteString("   3) Termius (install from termius.com, NOT the App Store): New Host with the\n")
	s.WriteString("      fields above, attach your key, enable Mosh, set the startup command.\n")
	s.WriteString("      Then every time: open Termius -> tap the host -> you're in tmux, coding.\n")
	fmt.Fprintf(&s, "   Full guide in the README. `ssh %s` also works from any terminal once saved.\n", b.Name)
	return s.String()
}

func qrBlock(text string) string {
	var buf bytes.Buffer
	qrterminal.GenerateHalfBlock(text, qrterminal.L, &buf)
	return buf.String()
}

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", "config"), nil
}

// WriteSSHConfig idempotently writes the box's Host block into ~/.ssh/config
// (replacing any prior block for the same name) and returns the file path.
func WriteSSHConfig(b Box) (string, error) {
	path, err := configPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	existing, _ := os.ReadFile(path)
	body := strings.TrimRight(removeBlock(string(existing), b.Name), "\n")
	if body != "" {
		body += "\n\n"
	}
	body += b.sshConfigBlock()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// RemoveSSHConfig deletes the box's block from ~/.ssh/config (used on destroy).
func RemoveSSHConfig(name string) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return nil // nothing to remove
	}
	body := strings.TrimRight(removeBlock(string(existing), name), "\n")
	if body != "" {
		body += "\n"
	}
	return os.WriteFile(path, []byte(body), 0o600)
}

// removeBlock strips the fenced pocketdev block for name (inclusive of markers).
func removeBlock(content, name string) string {
	begin, end := beginMarker(name), endMarker(name)
	var out []string
	skip := false
	for line := range strings.SplitSeq(content, "\n") {
		switch strings.TrimSpace(line) {
		case begin:
			skip = true
			continue
		case end:
			if skip {
				skip = false
				continue
			}
		}
		if !skip {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
