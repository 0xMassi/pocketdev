// Command pocketdev provisions an always-on, Tailscale-only Hetzner box that runs
// the AI coding CLI you already pay for — reachable from your laptop and phone.
//
//	pocketdev            guided create (TUI)
//	pocketdev destroy    delete the box + firewall, revoke the tailnet node
//	pocketdev version    print version
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"

	"github.com/0xMassi/pocketdev/internal/config"
	"github.com/0xMassi/pocketdev/internal/mobile"
	"github.com/0xMassi/pocketdev/internal/provision"
	"github.com/0xMassi/pocketdev/internal/tui"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"
)

// version is stamped by the release build (-ldflags "-X main.version=vX.Y.Z").
// When empty (go install / go build from source) it's resolved from the module's
// build info, so `pocketdev version` reports the real installed version, not a
// hardcoded guess.
var version = ""

func resolveVersion() string {
	if version != "" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v // a real tag (vX.Y.Z) or a pseudo-version from `go install`
	}
	// Built from a source checkout: fall back to the VCS revision.
	var rev, dirty string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev != "" {
		if len(rev) > 12 {
			rev = rev[:12]
		}
		return "dev-" + rev + dirty
	}
	return "dev"
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	var err error
	switch cmd {
	case "", "up", "create":
		err = tui.Run(ctx)
	case "destroy", "down":
		err = runDestroy(ctx)
	case "mobile", "phone":
		cfg, _ := config.Load()
		if h := flagValue(os.Args[2:], "--sshid"); h != "" {
			cfg.SSHIDHandle = h
		}
		tui.ShowMobile(ctx, cfg)
	case "version", "-v", "--version":
		fmt.Println("pocketdev", resolveVersion())
	default:
		usage()
	}

	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runDestroy(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	confirm := false
	if err := huh.NewConfirm().
		Title(fmt.Sprintf("Destroy box %q? This deletes the server and stops billing.", cfg.Name)).
		Value(&confirm).Run(); err != nil {
		return err
	}
	if !confirm {
		fmt.Println("cancelled")
		return nil
	}
	var derr error
	_ = spinner.New().Title(" Deleting server + firewall, revoking tailnet node").
		Action(func() { derr = provision.Destroy(ctx, cfg) }).Run()
	if derr != nil {
		return derr
	}
	_ = mobile.RemoveSSHConfig(cfg.Name) // tidy up the ~/.ssh/config entry
	fmt.Println("done")
	return nil
}

// flagValue returns the value for a `--name value` or `--name=value` flag in args.
func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v
		}
	}
	return ""
}

func usage() {
	fmt.Print(`pocketdev — your AI coding box, reachable from your phone

usage:
  pocketdev            guided create (default)
  pocketdev mobile     phone setup: authorize a key (Termius SSH ID) + write ~/.ssh/config
                       (pocketdev mobile --sshid <handle> to skip the prompt)
  pocketdev destroy    delete the box + firewall, revoke the tailnet node
  pocketdev version
`)
}
