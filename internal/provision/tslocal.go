package provision

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// tailscaleBin finds the local Tailscale CLI (PATH, then the macOS app bundle).
func tailscaleBin() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	for _, c := range []string{
		"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
		"/usr/local/bin/tailscale",
	} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

type tsNode struct {
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"` // MagicDNS name, unique per node ("devbox-1.tailnet.ts.net.")
	TailscaleIPs []string `json:"TailscaleIPs"`
	Online       bool     `json:"Online"`
	Created      string   `json:"Created"` // RFC3339; newest wins among same-name nodes
}

type tsStatus struct {
	Peer map[string]*tsNode `json:"Peer"`
}

// dnsLabel is the first segment of a MagicDNS name: "devbox-1.tail.ts.net." -> "devbox-1".
func dnsLabel(dnsName string) string {
	s := strings.TrimSuffix(dnsName, ".")
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return s[:i]
	}
	return s
}

// selectNode picks the box's node from the tailnet peers: online, whose MagicDNS
// label is the exact name or Tailscale's collision rename `name-<digits>`, and —
// if an old same-name node lingers — the most recently created one (the new box).
func selectNode(peers map[string]*tsNode, name string) *tsNode {
	renamed := regexp.MustCompile(`^` + regexp.QuoteMeta(name) + `-\d+$`)
	var best *tsNode
	for _, n := range peers {
		if n == nil || !n.Online {
			continue
		}
		label := dnsLabel(n.DNSName)
		if label == "" {
			label = n.HostName
		}
		if label != name && !renamed.MatchString(label) {
			continue
		}
		if best == nil || n.Created > best.Created { // RFC3339 sorts lexically
			best = n
		}
	}
	return best
}

// detectNode returns the 100.x address and tailnet label of the box. It matches on
// the unique MagicDNS label (HostName is NOT unique across recreated nodes), accepts
// the exact name or Tailscale's collision rename `name-<digits>`, and — when an old
// same-name node lingers — picks the most recently CREATED online node (the new box).
func detectNode(ctx context.Context, bin, name string) (ip, host string) {
	n := tailnetNode(ctx, bin, name)
	if n == nil {
		return "", ""
	}
	host = dnsLabel(n.DNSName)
	if host == "" {
		host = n.HostName
	}
	return nodeIP(n), host
}

func tailnetNode(ctx context.Context, bin, name string) *tsNode {
	out, err := exec.CommandContext(ctx, bin, "status", "--json").Output()
	if err != nil {
		return nil
	}
	var st tsStatus
	if json.Unmarshal(out, &st) != nil {
		return nil
	}
	return selectNode(st.Peer, name)
}

func nodeIP(n *tsNode) string {
	for _, a := range n.TailscaleIPs {
		if strings.HasPrefix(a, "100.") {
			return a
		}
	}
	return ""
}

// ResolveFQDN returns the box's full MagicDNS FQDN (no trailing dot) and 100.x IP
// from the local Tailscale CLI. Empty strings if it can't be found.
func ResolveFQDN(ctx context.Context, name string) (fqdn, ip string) {
	bin := tailscaleBin()
	if bin == "" {
		return "", ""
	}
	n := tailnetNode(ctx, bin, name)
	if n == nil {
		return "", ""
	}
	return strings.TrimSuffix(n.DNSName, "."), nodeIP(n)
}

// waitLocalNode polls the local CLI until the box's node is online. Best-effort:
// returns ("","") if the CLI is missing or the node never appears in time.
func waitLocalNode(ctx context.Context, name string, attempts int, every time.Duration) (ip, host string) {
	bin := tailscaleBin()
	if bin == "" {
		return "", ""
	}
	for range attempts {
		if ip, host = detectNode(ctx, bin, name); ip != "" {
			return ip, host
		}
		select {
		case <-ctx.Done():
			return "", ""
		case <-time.After(every):
		}
	}
	return ip, host
}
