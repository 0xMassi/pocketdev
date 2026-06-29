// Package tui is the laptop-side guided experience: a sequence of small huh steps
// (built on Bubble Tea) followed by spinner-driven provisioning. Each step runs on
// its own, so browser pages open only when you reach the step that needs them.
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/0xMassi/pocketdev/internal/agents"
	"github.com/0xMassi/pocketdev/internal/config"
	"github.com/0xMassi/pocketdev/internal/hetzner"
	"github.com/0xMassi/pocketdev/internal/mobile"
	"github.com/0xMassi/pocketdev/internal/provision"
	"github.com/0xMassi/pocketdev/internal/tailscale"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	stepStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	boxStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 2).BorderForeground(lipgloss.Color("212"))
)

// Run drives the full create flow, one step at a time.
func Run(ctx context.Context) error {
	fmt.Println(titleStyle.Render("pocketdev") + dimStyle.Render("  your AI coding box, reachable from your phone"))

	cfg, _ := config.Load()

	// Persist after every step (0600) so credentials you already entered are
	// reused on the next run even if you abort or hit an error mid-flow.
	warned := false
	persist := func() {
		if err := config.Save(cfg); err != nil && !warned {
			warned = true
			fmt.Println(dimStyle.Render("warning: could not save config: " + err.Error()))
		}
	}

	// Order mirrors the real dependency chain: get into Hetzner, pick the box,
	// secure it with Tailscale, choose what to run on it, then optional project.
	if err := stepHetzner(ctx, &cfg); err != nil {
		return err
	}
	persist()
	hz := hetzner.New(cfg.HCloudToken)
	if err := stepBox(ctx, hz, &cfg); err != nil {
		return err
	}
	persist()
	if err := stepTailscale(ctx, &cfg); err != nil {
		return err
	}
	persist()
	if err := stepAgents(&cfg); err != nil {
		return err
	}
	if err := stepProject(ctx, &cfg); err != nil {
		return err
	}
	if !cfg.UseExisting { // adopted boxes use your existing SSH auth, no injected key
		if err := stepSSHKey(&cfg); err != nil {
			return err
		}
	}
	persist()

	ok, err := stepReview(&cfg)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println(dimStyle.Render("cancelled — nothing was created"))
		return nil
	}

	persist()
	return deploy(ctx, cfg)
}

// --- steps ---

func stepAgents(cfg *config.Config) error {
	header("Agents", "Installed on the box, not your laptop. Each logs in with your own subscription.")
	return run(huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title("Which AI coding CLI(s) to install on the box?").
			Options(agentOptions(cfg.Agents)...).
			Value(&cfg.Agents).
			Validate(func(s []string) error {
				if len(s) == 0 {
					return fmt.Errorf("pick at least one")
				}
				return nil
			}),
	))
}

func stepProject(ctx context.Context, cfg *config.Config) error {
	header("Project", "What code should the box start with?")
	for {
		src := cfg.Source
		if src == "" {
			src = "fresh"
		}
		if err := run(huh.NewGroup(
			huh.NewSelect[string]().Title("Source").
				Options(
					huh.NewOption("Clone a GitHub repo", "github"),
					huh.NewOption("Copy a local folder (rsync)", "rsync"),
					huh.NewOption("Start fresh (empty box)", "fresh"),
				).Value(&src),
		)); err != nil {
			return err
		}

		switch src {
		case "fresh":
			cfg.Source, cfg.GitHub, cfg.RepoURL, cfg.LocalPath = "fresh", false, "", ""
			return nil

		case "github":
			cfg.Source, cfg.GitHub, cfg.LocalPath = "github", true, ""
			var repos []ghRepo
			var lerr error
			_ = check(ctx, "Listing your GitHub repos", func() error {
				repos, lerr = listRepos(ctx)
				return nil // never fail the spinner; fall back to manual input
			})
			manual := false
			switch {
			case lerr == nil && len(repos) > 0:
				name, back, err := pickRepo(repos, cfg.RepoURL)
				if err != nil {
					return err
				}
				if back {
					continue
				}
				if name == manualRepo {
					manual = true
				} else {
					cfg.RepoURL = name
				}
			case lerr != nil:
				fmt.Println(dimStyle.Render("  gh not available locally — enter the repo manually."))
				manual = true
			default:
				fmt.Println(dimStyle.Render("  No repos found for your gh account — enter one manually."))
				manual = true
			}
			if manual {
				if err := run(huh.NewGroup(
					huh.NewInput().Title("Repo (owner/repo or URL)").Value(&cfg.RepoURL).Validate(notEmpty("repo")),
				)); err != nil {
					return err
				}
			}
			return nil

		case "rsync":
			cfg.Source, cfg.GitHub, cfg.RepoURL = "rsync", false, ""
			home, _ := os.UserHomeDir()
			start := home
			if cfg.LocalPath != "" {
				start = filepath.Dir(cfg.LocalPath)
			}
			path, back, err := pickLocalPath(start)
			if err != nil {
				return err
			}
			if back {
				continue
			}
			cfg.LocalPath = path
			return nil
		}
	}
}

func stepBox(ctx context.Context, hz *hetzner.Client, cfg *config.Config) error {
	header("Box", "Create a fresh box, or adopt a server you already own.")
	if err := run(huh.NewGroup(
		huh.NewSelect[bool]().Title("How do you want the box?").
			Options(
				huh.NewOption("Create a new Hetzner box", false),
				huh.NewOption("Adopt a server I already own (SSH, no rebuild)", true),
			).Value(&cfg.UseExisting),
	)); err != nil {
		return err
	}
	if cfg.UseExisting {
		handled, err := stepAdopt(ctx, hz, cfg)
		if err != nil {
			return err
		}
		if handled {
			return nil
		}
		// no servers found -> fell back to create
	}
	return stepCreate(ctx, hz, cfg)
}

func stepCreate(ctx context.Context, hz *hetzner.Client, cfg *config.Config) error {
	var cat *hetzner.Catalog
	if err := check(ctx, "Loading sizes + live prices", func() error {
		var e error
		cat, e = hz.TypeCatalog(ctx)
		return e
	}); err != nil {
		return err
	}
	all := cat.All()
	if len(all) == 0 {
		return fmt.Errorf("no server types available on this account")
	}

	if err := run(huh.NewGroup(
		huh.NewInput().Title("Box name").Description("SSH/MagicDNS host: ssh dev@<name>").
			Value(&cfg.Name).Validate(validHostname),
	)); err != nil {
		return err
	}

	// Drill down Type -> Class -> Architecture, then show the size table. The
	// table's "back" returns here so you can re-pick the class.
	for {
		leaf, err := chooseLeaf(all, cfg)
		if err != nil {
			return err
		}
		key, back, err := pickSize(leaf, defaultSel(leaf, cfg.ServerType, cfg.Region))
		if err != nil {
			return err
		}
		if back {
			continue
		}
		cfg.ServerType, cfg.Region = splitSel(key)
		if cfg.ServerType == "" {
			return fmt.Errorf("no size selected")
		}
		return nil
	}
}

// chooseLeaf walks Type -> Class -> Architecture, prompting only where there is
// more than one option, and returns the matching size set.
func chooseLeaf(all []hetzner.SizeOption, cfg *config.Config) ([]hetzner.SizeOption, error) {
	dGroup, dClass, dArch := defaultsFor(all, cfg.ServerType)

	group, err := selectOne("Type", "Shared = cheaper, variable CPU. Dedicated = consistent performance.",
		distinctGroups(all), dGroup)
	if err != nil {
		return nil, err
	}
	class, err := selectOne("Class", classDesc(group), distinctClasses(all, group), dClass)
	if err != nil {
		return nil, err
	}
	arch, err := selectOne("Architecture", "CPU architecture for this class.",
		distinctArches(all, group, class), dArch)
	if err != nil {
		return nil, err
	}
	return filterLeaf(all, group, class, arch), nil
}

func stepAdopt(ctx context.Context, hz *hetzner.Client, cfg *config.Config) (bool, error) {
	var servers []hetzner.ExistingServer
	if err := check(ctx, "Listing your Hetzner servers", func() error {
		var e error
		servers, e = hz.ListServers(ctx)
		return e
	}); err != nil {
		return false, err
	}
	if len(servers) == 0 {
		fmt.Println(dimStyle.Render("  No servers in this project yet — creating a new one instead."))
		cfg.UseExisting = false
		return false, nil
	}

	byName := map[string]hetzner.ExistingServer{}
	var opts []huh.Option[string]
	for _, s := range servers {
		byName[s.Name] = s
		ip := s.IPv4
		if !s.HasIPv4 {
			ip = "no public IPv4"
		}
		managed := ""
		if s.Managed {
			managed = "  (pocketdev)"
		}
		opts = append(opts, huh.NewOption(
			fmt.Sprintf("%-16s %-7s %-5s %-8s %s%s", s.Name, s.Type, s.Location, s.Status, ip, managed),
			s.Name))
	}
	if err := run(huh.NewGroup(
		huh.NewSelect[string]().Title("Which server to adopt?").
			Description("pocketdev installs software and joins it to your tailnet — pick carefully.").
			Options(opts...).Value(&cfg.ExistingServer),
	)); err != nil {
		return false, err
	}

	chosen := byName[cfg.ExistingServer]
	if chosen.Status != "running" {
		return false, fmt.Errorf("server %q is %s — power it on in the Hetzner console first", chosen.Name, chosen.Status)
	}
	cfg.Name = chosen.Name
	cfg.SSHHost = chosen.IPv4
	if !chosen.HasIPv4 {
		cfg.SSHHost = chosen.Name // reach it by MagicDNS once it's on the tailnet
	}
	if cfg.SSHUser == "" {
		cfg.SSHUser = "root"
	}
	if err := run(huh.NewGroup(
		huh.NewInput().Title("SSH host").Description("IPv4, or a tailnet/MagicDNS name").Value(&cfg.SSHHost).Validate(notEmpty("host")),
		huh.NewInput().Title("SSH login user").Description("root, or a user with passwordless sudo").Value(&cfg.SSHUser).Validate(notEmpty("user")),
	)); err != nil {
		return false, err
	}
	return true, nil
}

// selectOne prompts with a huh select, or returns the sole option without asking.
func selectOne(title, desc string, opts []string, def string) (string, error) {
	switch len(opts) {
	case 0:
		return "", fmt.Errorf("no %s available", strings.ToLower(title))
	case 1:
		return opts[0], nil
	}
	v := def
	if !slices.Contains(opts, v) {
		v = opts[0]
	}
	ho := make([]huh.Option[string], 0, len(opts))
	for _, o := range opts {
		ho = append(ho, huh.NewOption(o, o))
	}
	err := run(huh.NewGroup(
		huh.NewSelect[string]().Title(title).Description(desc).Options(ho...).Value(&v),
	))
	return v, err
}

func classDesc(group string) string {
	if group == hetzner.GroupDedicated {
		return "Dedicated vCPU on the latest hardware."
	}
	return "Cost-Optimized = older HW, limited availability. Regular = newer HW."
}

func distinctGroups(all []hetzner.SizeOption) []string {
	return present([]string{hetzner.GroupShared, hetzner.GroupDedicated}, all,
		func(o hetzner.SizeOption) string { return o.Group })
}

func distinctClasses(all []hetzner.SizeOption, group string) []string {
	in := filterBy(all, func(o hetzner.SizeOption) bool { return o.Group == group })
	return present([]string{hetzner.ClassCostOptimized, hetzner.ClassRegular, hetzner.ClassGeneral}, in,
		func(o hetzner.SizeOption) string { return o.Class })
}

func distinctArches(all []hetzner.SizeOption, group, class string) []string {
	in := filterBy(all, func(o hetzner.SizeOption) bool { return o.Group == group && o.Class == class })
	return present([]string{hetzner.ArchX86Intel, hetzner.ArchX86AMD, hetzner.ArchArm}, in,
		func(o hetzner.SizeOption) string { return o.ArchLabel })
}

func filterLeaf(all []hetzner.SizeOption, group, class, arch string) []hetzner.SizeOption {
	return filterBy(all, func(o hetzner.SizeOption) bool {
		return o.Group == group && o.Class == class && o.ArchLabel == arch
	})
}

func defaultsFor(all []hetzner.SizeOption, serverType string) (group, class, arch string) {
	for _, o := range all {
		if o.Type == serverType {
			return o.Group, o.Class, o.ArchLabel
		}
	}
	return "", "", ""
}

func filterBy(in []hetzner.SizeOption, keep func(hetzner.SizeOption) bool) []hetzner.SizeOption {
	var out []hetzner.SizeOption
	for _, o := range in {
		if keep(o) {
			out = append(out, o)
		}
	}
	return out
}

// present returns the values in order that appear at least once in the set.
func present(order []string, in []hetzner.SizeOption, key func(hetzner.SizeOption) string) []string {
	seen := map[string]bool{}
	for _, o := range in {
		seen[key(o)] = true
	}
	var out []string
	for _, s := range order {
		if seen[s] {
			out = append(out, s)
		}
	}
	return out
}

func validSel(opts []hetzner.SizeOption, sel string) bool {
	for _, o := range opts {
		if o.Type+"|"+o.Region == sel {
			return true
		}
	}
	return false
}

// defaultSel returns the saved type+region if still available, else the cheapest.
func defaultSel(opts []hetzner.SizeOption, curType, curRegion string) string {
	if key := curType + "|" + curRegion; validSel(opts, key) {
		return key
	}
	if len(opts) > 0 {
		return opts[0].Type + "|" + opts[0].Region
	}
	return ""
}

func splitSel(s string) (serverType, region string) {
	if i := strings.IndexByte(s, '|'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// countryFlag turns an ISO-3166 alpha-2 code into its flag emoji ("DE" -> 🇩🇪).
func countryFlag(cc string) string {
	cc = strings.ToUpper(strings.TrimSpace(cc))
	if len(cc) != 2 {
		return ""
	}
	out := make([]rune, 0, 2)
	for _, c := range cc {
		if c < 'A' || c > 'Z' {
			return ""
		}
		out = append(out, 0x1F1E6+(c-'A'))
	}
	return string(out)
}

// regionLabel renders a region as "🇩🇪 Falkenstein (fsn1)".
func regionLabel(o hetzner.SizeOption) string {
	city := o.City
	if city == "" {
		city = strings.ToUpper(o.Region)
	}
	s := city + " (" + o.Region + ")"
	if f := countryFlag(o.Country); f != "" {
		s = f + " " + s
	}
	return s
}

// priceLabel renders the net monthly price, e.g. "EUR 19.49/mo".
func priceLabel(o hetzner.SizeOption) string {
	if o.Monthly <= 0 {
		return "n/a"
	}
	cur := o.Currency
	if cur != "" {
		cur += " "
	}
	return fmt.Sprintf("%s%.2f/mo", cur, o.Monthly)
}

func stepSSHKey(cfg *config.Config) error {
	pub, err := ensureSSHKey()
	if err != nil {
		return err
	}
	cfg.SSHPublicKeys = dedupeKeys([]string{pub})
	fmt.Println(okStyle.Render("  ✓ ") + "SSH key ~/.ssh/id_ed25519.pub")
	return nil
}

func stepHetzner(ctx context.Context, cfg *config.Config) error {
	if cfg.HCloudToken != "" {
		if err := check(ctx, "Checking saved Hetzner token", func() error {
			return hetzner.New(cfg.HCloudToken).Validate(ctx)
		}); err == nil {
			fmt.Println(okStyle.Render("  ✓ ") + "Hetzner token (saved/env)")
			return nil
		}
		cfg.HCloudToken = "" // stale; re-ask below
	}
	// Reuse a token from the official hcloud CLI if one is configured.
	if tok, name, ok := config.HCloudCLIToken(); ok {
		if confirmYes(fmt.Sprintf("Found your hcloud CLI context %q — use its token?", name)) {
			cfg.HCloudToken = tok
			if err := check(ctx, "Checking hcloud CLI token", func() error {
				return hetzner.New(cfg.HCloudToken).Validate(ctx)
			}); err == nil {
				fmt.Println(okStyle.Render("  ✓ ") + "Hetzner token (from hcloud CLI)")
				return nil
			}
			fmt.Println(errStyle.Render("  ✗ ") + "that token didn't validate; enter one manually")
			cfg.HCloudToken = ""
		}
	}
	header("Hetzner Cloud", hetznerHelp)
	if askOpen("Open the Hetzner console in your browser?") {
		openURL("https://console.hetzner.com/")
	}
	for {
		if err := run(huh.NewGroup(
			huh.NewInput().Title("Paste your Hetzner token (Read & Write)").
				EchoMode(huh.EchoModePassword).Value(&cfg.HCloudToken).Validate(notEmpty("token")),
		)); err != nil {
			return err
		}
		err := check(ctx, "Checking token", func() error { return hetzner.New(cfg.HCloudToken).Validate(ctx) })
		if err == nil {
			fmt.Println(okStyle.Render("  ✓ ") + "token works")
			return nil
		}
		fmt.Println(errStyle.Render("  ✗ ") + "token rejected: " + err.Error() + " (try again)")
		cfg.HCloudToken = ""
	}
}

func stepTailscale(ctx context.Context, cfg *config.Config) error {
	if cfg.TSAuthKey != "" {
		fmt.Println(okStyle.Render("  ✓ ") + "Tailscale auth key (saved/env)")
		return nil
	}
	// Advanced/zero-touch path: an OAuth client supplied via env or saved config.
	if cfg.TSOAuthID != "" && cfg.TSOAuthSecret != "" {
		if err := check(ctx, "Checking saved Tailscale client", func() error {
			return tailscale.Validate(ctx, cfg.TSOAuthID, cfg.TSOAuthSecret)
		}); err == nil {
			fmt.Println(okStyle.Render("  ✓ ") + "Tailscale OAuth client (saved/env)")
			return nil
		}
		cfg.TSOAuthID, cfg.TSOAuthSecret = "", ""
	}
	// Default: one reusable auth key. No ACL, OAuth, or MagicDNS setup needed.
	header("Tailscale", tailscaleHelp)
	if askOpen("Open the Tailscale keys page now?") {
		openURL("https://login.tailscale.com/admin/settings/keys")
	}
	if err := run(huh.NewGroup(
		huh.NewInput().Title("Paste a Tailscale auth key").
			Description("Settings -> Keys -> Generate, toggle Reusable. Starts with tskey-.").
			EchoMode(huh.EchoModePassword).Value(&cfg.TSAuthKey).Validate(validAuthKey),
	)); err != nil {
		return err
	}
	fmt.Println(okStyle.Render("  ✓ ") + "auth key saved")
	return nil
}

func validAuthKey(s string) error {
	if !strings.HasPrefix(strings.TrimSpace(s), "tskey-") {
		return fmt.Errorf("a Tailscale auth key starts with tskey-")
	}
	return nil
}

// check runs fn under a spinner and returns its error.
func check(ctx context.Context, title string, fn func() error) error {
	var err error
	_ = spinner.New().Context(ctx).Title(" " + title).Action(func() { err = fn() }).Run()
	return err
}

func stepReview(cfg *config.Config) (bool, error) {
	header("Review", "")
	fmt.Printf("  agents:  %s\n", strings.Join(cfg.Agents, ", "))
	title, desc := "Create the box now?", "Starts billing a Hetzner server. Tear down later: pocketdev destroy"
	if cfg.UseExisting {
		fmt.Printf("  adopt:   %s  via %s@%s\n", cfg.Name, cfg.SSHUser, cfg.SSHHost)
		fmt.Println(dimStyle.Render("  installs software + joins your tailnet on a machine you already own"))
		title = "Adopt this server now?"
		desc = "Runs setup on your existing server (pocketdev destroy will NOT delete it)."
	} else {
		fmt.Printf("  box:     %s  (%s, %s)\n", cfg.Name, cfg.ServerType, cfg.Region)
	}
	switch cfg.Source {
	case "github":
		fmt.Printf("  project: clone %s\n", cfg.RepoURL)
	case "rsync":
		fmt.Printf("  project: rsync %s\n", cfg.LocalPath)
	case "fresh":
		fmt.Println("  project: empty box")
	}
	fmt.Println()
	ok := true
	err := run(huh.NewGroup(huh.NewConfirm().Title(title).Description(desc).Value(&ok)))
	return ok, err
}

// deploy runs the provisioning steps, branching on create vs adopt.
func deploy(ctx context.Context, cfg config.Config) error {
	p, err := provision.New(cfg)
	if err != nil {
		return err
	}
	type step struct {
		title  string
		fn     func() error
		stream bool // run without a spinner so remote output is visible
	}
	var steps []step
	if cfg.UseExisting {
		steps = []step{
			{"Validating credentials", func() error { return p.ValidateCredentials(ctx) }, false},
			{"Minting a short-lived Tailscale key", func() error { return p.PrepareTailscale(ctx) }, false},
			{"Bootstrapping the existing server over SSH", func() error { return p.AdoptServer(ctx) }, true},
			{"Waiting for the box to join your tailnet", func() error { _, e := p.WaitForTailnet(ctx); return e }, false},
		}
	} else {
		steps = []step{
			{"Validating Hetzner + Tailscale credentials", func() error { return p.ValidateCredentials(ctx) }, false},
			{"Minting a short-lived Tailscale key", func() error { return p.PrepareTailscale(ctx) }, false},
			{"Rendering first-boot setup", func() error { return p.Render() }, false},
			{"Creating firewall + server", func() error { return p.CreateServer(ctx) }, false},
			{"Waiting for the box to join your tailnet", func() error { _, e := p.WaitForTailnet(ctx); return e }, false},
		}
	}
	if cfg.Source == "rsync" {
		steps = append(steps, step{"Copying your folder to the box (rsync)", func() error { return p.SyncLocal(ctx) }, true})
	}
	fmt.Println()
	for _, s := range steps {
		var stepErr error
		if s.stream {
			fmt.Println(dimStyle.Render("  " + s.title + " ..."))
			stepErr = s.fn()
		} else {
			_ = spinner.New().Context(ctx).Title(" " + s.title).Action(func() { stepErr = s.fn() }).Run()
		}
		if stepErr != nil {
			fmt.Println(errStyle.Render("  ✗ ") + s.title)
			return stepErr
		}
		fmt.Println(okStyle.Render("  ✓ ") + s.title)
	}

	connectAndVerify(ctx, cfg, p)
	return nil
}

// connectAndVerify waits for first-boot, checks the install over SSH, then offers
// to drop into an interactive `pocketdev setup` (agent logins + clone).
func connectAndVerify(ctx context.Context, cfg config.Config, p *provision.Provisioner) {
	fmt.Println()
	if !cfg.UseExisting {
		if err := watchFirstBoot(ctx, p); err != nil {
			fmt.Println(dimStyle.Render("  note: " + err.Error()))
		} else {
			fmt.Println(okStyle.Render("  ✓ ") + "first-boot complete (apt upgrade + installs)")
		}
	}

	var checks []provision.Check
	var verr error
	_ = spinner.New().Context(ctx).Title(" Verifying the box over SSH").
		Action(func() { checks, verr = p.Verify(ctx) }).Run()
	if verr != nil && len(checks) == 0 {
		fmt.Println(dimStyle.Render("  couldn't verify over SSH yet (" + verr.Error() + ")"))
	}
	for _, c := range checks {
		mark := okStyle.Render("  ✓ ")
		if !c.OK {
			mark = errStyle.Render("  ✗ ")
		}
		fmt.Println(mark + c.Name)
	}

	// Show the box summary + phone card BEFORE connecting. The final step logs in
	// your agent, which takes over the terminal — anything printed after that is
	// hidden until you quit the agent. So save your QR / ssh-config first, then
	// drop into setup. The agent is the last thing to turn on, by design.
	printResult(cfg, p)
	ShowMobile(ctx, cfg)

	user, host := p.SSHUserHost()
	prompt := fmt.Sprintf("Finish setup now? Connects to %s@%s, clones your repo, then logs in your agent (this opens the agent and is the last step).", user, host)
	if confirmYes(prompt) {
		if err := p.Connect(ctx); err != nil {
			fmt.Println(dimStyle.Render("  ssh ended: " + err.Error()))
		}
	} else {
		fmt.Println(dimStyle.Render("  later: ssh " + cfg.Name + "  then  pocketdev setup"))
	}
}

// ShowMobile prints the "Code from your phone" card (QR + steps) and offers to
// add the box to ~/.ssh/config. Used after a create and by `pocketdev mobile`.
func ShowMobile(ctx context.Context, cfg config.Config) {
	fqdn, ip := provision.ResolveFQDN(ctx, cfg.Name)
	if fqdn == "" && ip == "" {
		fmt.Println(dimStyle.Render("  couldn't find " + cfg.Name + " on your tailnet — is Tailscale running on this laptop?"))
		return
	}
	user := "dev"
	if cfg.UseExisting {
		user = cfg.SSHUser
	}
	box := mobile.Box{Name: cfg.Name, FQDN: fqdn, IP: ip, User: user, KeyPath: sshKeyPath()}
	fmt.Print(mobile.Card(box))

	// The box only trusts keys it already knows. A QR can't carry a private key,
	// so the phone needs a key the box trusts. SSH ID is the easy path: the phone
	// holds a FaceID-bound key it never exports, and we fetch its public half.
	host := ip
	if host == "" {
		host = fqdn
	}
	authorizePhoneKey(ctx, &cfg, user, host)

	if confirmYes("Add " + cfg.Name + " to ~/.ssh/config (so `ssh " + cfg.Name + "` works on this machine)?") {
		if path, err := mobile.WriteSSHConfig(box); err != nil {
			fmt.Println(dimStyle.Render("  could not write ssh config: " + err.Error()))
		} else {
			fmt.Println(okStyle.Render("  ✓ ") + "saved to " + path + "  —  now: ssh " + cfg.Name)
		}
	}
}

// authorizePhoneKey gets a key the box trusts onto the user's phone. SSH ID is
// the recommended path (no key files; the phone's key is FaceID-bound and never
// leaves it — we fetch only the public halves). Pasting a public key is the
// manual fallback. The SSH ID handle is persisted for next time.
func authorizePhoneKey(ctx context.Context, cfg *config.Config, user, host string) {
	const (
		mSSHID = "sshid"
		mPaste = "paste"
		mSkip  = "skip"
	)
	method := mSSHID
	_ = huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Give your phone a key the box trusts").
			Description("SSH ID authorizes all your devices from one handle; their keys never leave them.").
			Options(
				huh.NewOption("Termius SSH ID — recommended, no key files", mSSHID),
				huh.NewOption("Paste a public key — phone-generated, manual", mPaste),
				huh.NewOption("Skip for now", mSkip),
			).Value(&method),
	)).WithTheme(huh.ThemeCharm()).Run()

	switch method {
	case mSSHID:
		handle := cfg.SSHIDHandle
		_ = huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Your Termius SSH ID handle (the name in sshid.io/<handle>):").Value(&handle),
		)).WithTheme(huh.ThemeCharm()).Run()
		handle = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(handle), "@"))
		if handle == "" {
			fmt.Println(dimStyle.Render("  skipped — find your handle in Termius → SSH ID, then run `pocketdev mobile`"))
			return
		}
		var keys []string
		var ferr error
		_ = spinner.New().Context(ctx).Title(" Fetching your SSH ID public keys").
			Action(func() { keys, ferr = provision.FetchSSHIDKeys(ctx, handle) }).Run()
		if ferr != nil {
			fmt.Println(errStyle.Render("  ✗ ") + ferr.Error())
			return
		}
		added, aerr := provision.AuthorizeKeys(ctx, user, host, keys)
		if aerr != nil {
			fmt.Println(errStyle.Render("  ✗ ") + "could not authorize keys: " + aerr.Error())
			return
		}
		cfg.SSHIDHandle = handle
		_ = config.Save(*cfg)
		fmt.Printf("%s%d device key(s) from @%s authorized (%d new) — open Termius → tap host → FaceID → in\n",
			okStyle.Render("  ✓ "), len(keys), handle, added)
	case mPaste:
		pub := promptLine("Paste your phone's public key (ssh-ed25519 AAAA… you@phone):")
		if strings.TrimSpace(pub) == "" {
			fmt.Println(dimStyle.Render("  skipped — run `pocketdev mobile` again once you've made a key on the phone"))
			return
		}
		if err := provision.AuthorizeKey(ctx, user, host, pub); err != nil {
			fmt.Println(errStyle.Render("  ✗ ") + "could not authorize key: " + err.Error())
			return
		}
		fmt.Println(okStyle.Render("  ✓ ") + "phone key authorized — Termius can now connect with it")
	}
}

func sshKeyPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh", "id_ed25519")
}

// --- ui helpers ---

func header(title, desc string) {
	fmt.Println("\n" + stepStyle.Render("▸ "+title))
	if desc != "" {
		fmt.Println(dimStyle.Render(indent(desc)))
	}
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

// run executes a single-group form with the shared theme.
func run(g *huh.Group) error {
	return huh.NewForm(g).WithTheme(huh.ThemeCharm()).Run()
}

// askOpen shows a small confirm and returns whether the user chose to open.
func askOpen(title string) bool {
	open := false
	_ = huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(title).Affirmative("Open").Negative("Skip").Value(&open),
	)).WithTheme(huh.ThemeCharm()).Run()
	return open
}

// promptLine asks for a single line of free text (e.g. a pasted public key).
func promptLine(title string) string {
	var v string
	_ = huh.NewForm(huh.NewGroup(
		huh.NewInput().Title(title).Value(&v),
	)).WithTheme(huh.ThemeCharm()).Run()
	return strings.TrimSpace(v)
}

// confirmYes shows a Yes/No confirm defaulting to Yes.
func confirmYes(title string) bool {
	yes := true
	_ = huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(title).Affirmative("Yes").Negative("No").Value(&yes),
	)).WithTheme(huh.ThemeCharm()).Run()
	return yes
}

func agentOptions(selected []string) []huh.Option[string] {
	sel := map[string]bool{}
	for _, s := range selected {
		sel[s] = true
	}
	if len(sel) == 0 {
		sel["claude"] = true
	}
	var opts []huh.Option[string]
	for _, a := range agents.All {
		opts = append(opts, huh.NewOption(a.Name, a.Key).Selected(sel[a.Key]))
	}
	return opts
}

func printResult(cfg config.Config, p *provision.Provisioner) {
	sshUser := "dev"
	if cfg.UseExisting {
		sshUser = cfg.SSHUser
	}
	// Prefer the box's tailnet IP for SSH — it always works even if MagicDNS is
	// off or Tailscale renamed the box (e.g. devbox-1 after a recreate).
	name, ip := p.Node()
	host := ip
	if host == "" {
		host = name
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s is on your tailnet. No public SSH needed.\n\n", titleStyle.Render(name))
	fmt.Fprintf(&b, "  SSH (laptop, Tailscale up):  %s\n", okStyle.Render("ssh "+sshUser+"@"+host))
	if ip != "" {
		fmt.Fprintf(&b, "  Tailscale IP:                %s\n", ip)
	} else {
		fmt.Fprintf(&b, "  Tailscale IP:                %s\n", dimStyle.Render("run `tailscale status` in ~1 min"))
	}
	if name != cfg.Name {
		fmt.Fprintf(&b, "  %s\n", dimStyle.Render("note: an old '"+cfg.Name+"' node exists, so this box is '"+name+"' on the tailnet"))
	}
	b.WriteString("\nFinish on the box (one command):\n")
	fmt.Fprintf(&b, "  ssh %s@%s\n  %s\n\n", sshUser, host, okStyle.Render("pocketdev setup"))
	b.WriteString("Installs + logs in your agent(s):\n")
	for _, a := range p.Agents() {
		fmt.Fprintf(&b, "  • %s\n", a.Name)
	}
	switch cfg.Source {
	case "github":
		fmt.Fprintf(&b, "Then clones %s.\n", cfg.RepoURL)
	case "rsync":
		fmt.Fprintf(&b, "Your folder is already copied to the box.\n")
	}
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("Tear down (stops billing): pocketdev destroy"))
	fmt.Println("\n" + boxStyle.Render(b.String()))
}

// ensureSSHKey returns the laptop public key, generating one quietly if needed.
func ensureSSHKey() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	pubPath := filepath.Join(home, ".ssh", "id_ed25519.pub")
	if data, err := os.ReadFile(pubPath); err == nil {
		return strings.TrimSpace(string(data)), nil
	}
	keyPath := filepath.Join(home, ".ssh", "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return "", err
	}
	// Private key exists but the .pub is missing: derive it rather than letting
	// ssh-keygen prompt to overwrite the private key.
	if _, err := os.Stat(keyPath); err == nil {
		out, err := exec.Command("ssh-keygen", "-y", "-f", keyPath).Output()
		if err != nil {
			return "", fmt.Errorf("derive public key from %s: %w", keyPath, err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	// Quiet generation: -q and discarded output (no randomart dumped to the TUI).
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", keyPath, "-C", "pocketdev")
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ssh-keygen: %w", err)
	}
	data, err := os.ReadFile(pubPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func dedupeKeys(keys []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// hostnameRE is a DNS label: lowercase letters/digits/hyphens, 1-63 chars,
// starting and ending alphanumeric. The name flows into cloud-init shell, the
// Hetzner server name, and the MagicDNS host, so we keep it strict.
var hostnameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func validHostname(s string) error {
	if !hostnameRE.MatchString(s) {
		return fmt.Errorf("use lowercase letters, digits, hyphens (e.g. devbox)")
	}
	return nil
}

func notEmpty(what string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("%s is required", what)
		}
		return nil
	}
}

func openURL(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		return
	}
	go func() { _ = cmd.Wait() }() // reap the browser child so it doesn't linger as a zombie
}

const hetznerHelp = `Create a project API token (the one fiddly part):
1. Log in:        https://console.hetzner.com
2. Pick or create a Project, then click it to open it.
3. Left sidebar:  Security  ->  top tab "API tokens".
4. "Generate API token", name it, permission "Read & Write" (Read-only cannot create servers).
5. Copy it now — Hetzner shows the token only once.
Tip: if you use the hcloud CLI, run "hcloud context create pocketdev" and pocketdev reuses that token.
New accounts may need identity/payment verification before the first server.`

const tailscaleHelp = `Tailscale puts the box on your private network. One web step:
1. Sign in (first time):  https://login.tailscale.com/start
2. Settings -> Keys -> "Generate auth key", toggle "Reusable", copy it.
No ACL, OAuth, or MagicDNS setup needed.
(Advanced/zero-touch: set TS_OAUTH_CLIENT_ID + TS_OAUTH_CLIENT_SECRET instead.)`
