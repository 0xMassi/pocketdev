// Package hetzner wraps the Hetzner Cloud API for provisioning and tearing down
// the box: a default-deny firewall, an SSH key, and the server itself.
package hetzner

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// Hetzner's selection hierarchy: Type (Shared/Dedicated) -> Class -> Architecture.
const (
	GroupShared    = "Shared Resources"
	GroupDedicated = "Dedicated Resources"

	ClassCostOptimized = "Cost-Optimized"
	ClassRegular       = "Regular Performance"
	ClassGeneral       = "General Purpose"

	ArchX86Intel = "x86 (Intel)"
	ArchX86AMD   = "x86 (AMD)"
	ArchArm      = "Arm64 (Ampere)"
)

// Client is a thin wrapper around the hcloud client.
type Client struct{ c *hcloud.Client }

// New builds a client from a project API token.
func New(token string) *Client {
	return &Client{c: hcloud.NewClient(hcloud.WithToken(token))}
}

// Validate confirms the token works (read probe).
func (h *Client) Validate(ctx context.Context) error {
	_, err := h.c.ServerType.All(ctx)
	return err
}

// CreateOpts describes the server to create.
type CreateOpts struct {
	Name        string
	Region      string
	ServerType  string
	WithoutIPv4 bool
	UserData    string
	PublicKeys  []string
}

// Result is what Create returns to the caller.
type Result struct {
	IPv4 string // empty when WithoutIPv4
	IPv6 string
}

// EnsureFirewall returns a firewall with the given name, creating an empty
// (deny-all-inbound) one if it does not exist.
func (h *Client) ensureFirewall(ctx context.Context, name string) (*hcloud.Firewall, error) {
	fw, _, err := h.c.Firewall.GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if fw != nil {
		return fw, nil
	}
	res, _, err := h.c.Firewall.Create(ctx, hcloud.FirewallCreateOpts{Name: name})
	if err != nil {
		return nil, err
	}
	return res.Firewall, nil
}

// ensureSSHKeys uploads each public key if Hetzner doesn't already have it (by
// fingerprint) and returns the resulting key objects.
func (h *Client) ensureSSHKeys(ctx context.Context, name string, pubs []string) ([]*hcloud.SSHKey, error) {
	var keys []*hcloud.SSHKey
	for i, pub := range pubs {
		pub = strings.TrimSpace(pub)
		if pub == "" {
			continue
		}
		fp := fingerprint(pub)
		if fp != "" {
			if k, _, err := h.c.SSHKey.GetByFingerprint(ctx, fp); err == nil && k != nil {
				keys = append(keys, k)
				continue
			}
		}
		k, _, err := h.c.SSHKey.Create(ctx, hcloud.SSHKeyCreateOpts{
			Name:      fmt.Sprintf("%s-%d", name, i),
			PublicKey: pub,
		})
		if err != nil {
			return nil, fmt.Errorf("upload ssh key: %w", err)
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// Exists reports whether a server with the name already exists.
func (h *Client) Exists(ctx context.Context, name string) (bool, error) {
	s, _, err := h.c.Server.GetByName(ctx, name)
	return s != nil, err
}

// Create provisions the server with the firewall attached at create time (so
// there is never a window with open public ports).
func (h *Client) Create(ctx context.Context, o CreateOpts) (Result, error) {
	st, _, err := h.c.ServerType.GetByName(ctx, o.ServerType)
	if err != nil {
		return Result{}, err
	}
	if st == nil {
		return Result{}, fmt.Errorf("unknown server type %q", o.ServerType)
	}

	// Match the OS image arch to the server type (cax->arm, cx/cpx/ccx->x86).
	img, _, err := h.c.Image.GetByNameAndArchitecture(ctx, "ubuntu-24.04", st.Architecture)
	if err != nil {
		return Result{}, err
	}
	if img == nil {
		return Result{}, fmt.Errorf("ubuntu-24.04 image not found for %s", st.Architecture)
	}

	fw, err := h.ensureFirewall(ctx, o.Name+"-fw")
	if err != nil {
		return Result{}, err
	}
	keys, err := h.ensureSSHKeys(ctx, o.Name, o.PublicKeys)
	if err != nil {
		return Result{}, err
	}

	// A datacenter can be temporarily sold out of a type (resource_unavailable /
	// placement_error). Try the chosen region first, then same-zone siblings.
	var lastErr error
	for _, region := range candidateLocations(o.Region, st.Architecture) {
		loc, _, err := h.c.Location.GetByName(ctx, region)
		if err != nil || loc == nil {
			continue
		}
		res, _, err := h.c.Server.Create(ctx, hcloud.ServerCreateOpts{
			Name:       o.Name,
			ServerType: st,
			Image:      img,
			Location:   loc,
			UserData:   o.UserData,
			SSHKeys:    keys,
			Firewalls:  []*hcloud.ServerCreateFirewall{{Firewall: *fw}},
			Labels:     map[string]string{"purpose": "pocketdev"},
			PublicNet: &hcloud.ServerCreatePublicNet{
				EnableIPv4: !o.WithoutIPv4,
				EnableIPv6: true,
			},
		})
		if err != nil {
			if isUnavailable(err) {
				lastErr = err
				continue
			}
			return Result{}, err
		}
		// Placement can also fail asynchronously in the create action. Surface it
		// now; on unavailability, delete the half-created record and try the next.
		if err := h.c.Action.WaitFor(ctx, append([]*hcloud.Action{res.Action}, res.NextActions...)...); err != nil {
			if isUnavailable(err) {
				lastErr = err
				if s, _, e := h.c.Server.GetByName(ctx, o.Name); e == nil && s != nil {
					_, _, _ = h.c.Server.DeleteWithResult(ctx, s)
				}
				continue
			}
			return Result{}, err
		}

		out := Result{}
		if res.Server.PublicNet.IPv4.IP != nil {
			out.IPv4 = res.Server.PublicNet.IPv4.IP.String()
		}
		if res.Server.PublicNet.IPv6.IP != nil {
			out.IPv6 = res.Server.PublicNet.IPv6.IP.String()
		}
		return out, nil
	}
	return Result{}, fmt.Errorf("%s is sold out right now in every datacenter near %s — pick a different size or region and try again (Hetzner: %w)",
		o.ServerType, o.Region, lastErr)
}

// candidateLocations returns the chosen region first, then same-zone siblings
// (so an EU pick never silently lands in the US). ARM (cax) is EU-only.
func candidateLocations(chosen string, arch hcloud.Architecture) []string {
	zones := map[string][]string{
		"eu": {"nbg1", "fsn1", "hel1"},
		"us": {"ash", "hil"},
		"sg": {"sin"},
	}
	zoneOf := func(r string) string {
		for z, rs := range zones {
			if slices.Contains(rs, r) {
				return z
			}
		}
		return "eu"
	}
	siblings := zones[zoneOf(chosen)]
	if arch == hcloud.ArchitectureARM {
		siblings = zones["eu"] // CAX exists only in EU
	}
	out := []string{chosen}
	for _, r := range siblings {
		if r != chosen {
			out = append(out, r)
		}
	}
	return out
}

func isUnavailable(err error) bool {
	if hcloud.IsError(err, hcloud.ErrorCodeResourceUnavailable, hcloud.ErrorCodePlacementError) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "resource_unavailable") || strings.Contains(s, "placement")
}

// Destroy deletes the server and its firewall. Missing resources are ignored.
// The server delete is async; we wait for it before deleting the firewall,
// otherwise Hetzner rejects the firewall delete (409, still attached).
func (h *Client) Destroy(ctx context.Context, name string) error {
	if s, _, _ := h.c.Server.GetByName(ctx, name); s != nil {
		res, _, err := h.c.Server.DeleteWithResult(ctx, s)
		if err != nil {
			return err
		}
		if res != nil && res.Action != nil {
			if err := h.c.Action.WaitFor(ctx, res.Action); err != nil {
				return err
			}
		}
	}
	if fw, _, _ := h.c.Firewall.GetByName(ctx, name+"-fw"); fw != nil {
		if _, err := h.c.Firewall.Delete(ctx, fw); err != nil {
			return err
		}
	}
	return nil
}

// SizeOption is one buyable (server type, region) combination, classified into
// Hetzner's Type -> Class -> Architecture hierarchy.
type SizeOption struct {
	Type     string
	Region   string // location code, e.g. "nbg1"
	City     string // e.g. "Nuremberg"
	Country  string // ISO-3166 alpha-2, e.g. "DE"
	Cores    int
	MemoryGB float32
	DiskGB   int
	Monthly  float64 // net monthly price (ex VAT), in Currency
	Currency string  // account currency, e.g. EUR or USD

	Group     string // GroupShared | GroupDedicated
	Class     string // ClassCostOptimized | ClassRegular | ClassGeneral
	ArchLabel string // ArchX86Intel | ArchX86AMD | ArchArm
}

// classify maps a server type to Hetzner's hierarchy using the reliable CPUType
// and Architecture fields plus the family prefix (cx/cpx/cax/ccx) for the
// cost-optimized-vs-regular split, which has no dedicated API field.
func classify(st *hcloud.ServerType) (group, class, arch string) {
	family := st.Name
	for i, r := range st.Name {
		if r >= '0' && r <= '9' {
			family = st.Name[:i]
			break
		}
	}
	dedicated := st.CPUType == hcloud.CPUTypeDedicated
	arm := st.Architecture == hcloud.ArchitectureARM

	group = GroupShared
	if dedicated {
		group = GroupDedicated
	}
	switch {
	case dedicated:
		class = ClassGeneral
	case family == "cpx":
		class = ClassRegular
	default: // cx, cax, and any other shared family
		class = ClassCostOptimized
	}
	switch {
	case arm:
		arch = ArchArm
	case family == "cx":
		arch = ArchX86Intel
	default: // cpx, ccx
		arch = ArchX86AMD
	}
	return group, class, arch
}

// Catalog holds every available (type, region) option, classified.
type Catalog struct{ all []SizeOption }

// All returns the full option set (sorted cheapest-first within the same family).
func (c *Catalog) All() []SizeOption { return c.all }

// TypeCatalog fetches all server types once and builds the classified option set
// with per-region net monthly price. No size floor: the cheapest options show too.
func (h *Client) TypeCatalog(ctx context.Context) (*Catalog, error) {
	types, err := h.c.ServerType.All(ctx) // also fills Pricings + Locations
	if err != nil {
		return nil, fmt.Errorf("list server types: %w", err)
	}
	// Per-location prices omit the currency; it lives on the top-level pricing.
	currency := ""
	if pr, _, err := h.c.Pricing.Get(ctx); err == nil {
		currency = pr.Currency
	}
	// ServerType.Locations carries only the location code, not city/country, so
	// fetch the full location records once and join on the code.
	city := map[string]string{}
	country := map[string]string{}
	if locs, err := h.c.Location.All(ctx); err == nil {
		for _, l := range locs {
			city[l.Name] = l.City
			country[l.Name] = l.Country
		}
	}
	cat := &Catalog{}
	for _, st := range types {
		price := map[string]float64{}
		for _, p := range st.Pricings {
			if p.Location != nil {
				n, _ := strconv.ParseFloat(p.Monthly.Net, 64) // strings, must parse
				price[p.Location.Name] = n
			}
		}
		group, class, arch := classify(st)
		for _, l := range st.Locations {
			if l.Location == nil || !l.Available {
				continue
			}
			code := l.Location.Name
			cat.all = append(cat.all, SizeOption{
				Type: st.Name, Region: code, City: city[code], Country: country[code],
				Cores: st.Cores, MemoryGB: st.Memory, DiskGB: st.Disk,
				Monthly: price[code], Currency: currency,
				Group: group, Class: class, ArchLabel: arch,
			})
		}
	}
	sort.Slice(cat.all, func(i, j int) bool {
		if cat.all[i].Monthly != cat.all[j].Monthly {
			return cat.all[i].Monthly < cat.all[j].Monthly
		}
		if cat.all[i].Type != cat.all[j].Type {
			return cat.all[i].Type < cat.all[j].Type
		}
		return cat.all[i].Region < cat.all[j].Region
	})
	return cat, nil
}

// ExistingServer is a server already in the project, for the adopt path.
type ExistingServer struct {
	Name, Type, Location, Status, IPv4, IPv6 string
	HasIPv4                                  bool
	Managed                                  bool // already pocketdev-managed
}

// ListServers returns every server in the project.
func (h *Client) ListServers(ctx context.Context) ([]ExistingServer, error) {
	servers, err := h.c.Server.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	out := make([]ExistingServer, 0, len(servers))
	for _, s := range servers {
		e := ExistingServer{Name: s.Name, Status: string(s.Status)}
		if s.ServerType != nil {
			e.Type = s.ServerType.Name
		}
		if s.Location != nil { // Location, not the deprecated Datacenter
			e.Location = s.Location.Name
		}
		if !s.PublicNet.IPv4.IsUnspecified() && s.PublicNet.IPv4.IP != nil {
			e.IPv4, e.HasIPv4 = s.PublicNet.IPv4.IP.String(), true
		}
		if !s.PublicNet.IPv6.IsUnspecified() && s.PublicNet.IPv6.IP != nil {
			e.IPv6 = s.PublicNet.IPv6.IP.String()
		}
		e.Managed = s.Labels["purpose"] == "pocketdev"
		out = append(out, e)
	}
	return out, nil
}

// LabelAdopted tags an adopted server so it is recognizable, with an "adopted"
// marker so teardown refuses to delete a machine the user owned first.
func (h *Client) LabelAdopted(ctx context.Context, name string) error {
	s, _, err := h.c.Server.GetByName(ctx, name)
	if err != nil || s == nil {
		return err
	}
	labels := s.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	labels["purpose"], labels["adopted"] = "pocketdev", "true"
	_, _, err = h.c.Server.Update(ctx, s, hcloud.ServerUpdateOpts{Labels: labels})
	return err
}

// IsAdopted reports whether the named server carries the adopted marker.
func (h *Client) IsAdopted(ctx context.Context, name string) bool {
	s, _, err := h.c.Server.GetByName(ctx, name)
	return err == nil && s != nil && s.Labels["adopted"] == "true"
}

// fingerprint computes the MD5 fingerprint Hetzner uses to dedupe SSH keys.
func fingerprint(pub string) string {
	f := strings.Fields(pub)
	if len(f) < 2 {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil {
		return ""
	}
	sum := md5.Sum(raw)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(parts, ":")
}
