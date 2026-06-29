package provision

import "testing"

func TestDNSLabel(t *testing.T) {
	for in, want := range map[string]string{
		"devbox-1.tail1234.ts.net.": "devbox-1",
		"devbox.tail.ts.net":        "devbox",
		"devbox":                    "devbox",
	} {
		if got := dnsLabel(in); got != want {
			t.Errorf("dnsLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSelectNode(t *testing.T) {
	peers := map[string]*tsNode{
		// old box, same requested name, still briefly online, created earlier
		"a": {DNSName: "devbox.tail.ts.net.", Online: true, Created: "2026-06-29T10:00:00Z", TailscaleIPs: []string{"100.0.0.1"}},
		// new box, Tailscale renamed it devbox-1, created later -> should win
		"b": {DNSName: "devbox-1.tail.ts.net.", Online: true, Created: "2026-06-29T12:00:00Z", TailscaleIPs: []string{"100.0.0.2"}},
		// unrelated node sharing the prefix -> must NOT match
		"c": {DNSName: "devbox-laptop.tail.ts.net.", Online: true, Created: "2026-06-29T13:00:00Z"},
		// offline duplicate -> ignored
		"d": {DNSName: "devbox.tail.ts.net.", Online: false, Created: "2026-06-29T14:00:00Z"},
	}
	got := selectNode(peers, "devbox")
	if got == nil || dnsLabel(got.DNSName) != "devbox-1" {
		t.Fatalf("selectNode picked %v, want the newer devbox-1", got)
	}

	// Clean case: only the exact name exists.
	only := map[string]*tsNode{"a": {DNSName: "box.x.ts.net.", Online: true, Created: "1"}}
	if got := selectNode(only, "box"); got == nil || dnsLabel(got.DNSName) != "box" {
		t.Errorf("clean-case select = %v", got)
	}
	// No online match.
	if got := selectNode(map[string]*tsNode{"a": {DNSName: "box.x.ts.net.", Online: false}}, "box"); got != nil {
		t.Errorf("offline-only should select nil, got %v", got)
	}
}
