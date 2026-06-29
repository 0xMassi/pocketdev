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
	"strings"

	"github.com/0xMassi/pocketdev/internal/config"
	"github.com/0xMassi/pocketdev/internal/mobile"
	"github.com/0xMassi/pocketdev/internal/provision"
	"github.com/0xMassi/pocketdev/internal/tui"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"
)

var version = "0.1.0"

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
		fmt.Println("pocketdev", version)
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
