// A live first-boot progress view: an elapsed timer (so you can tell it's alive,
// not stuck) plus the current cloud-init activity streamed from the box.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/0xMassi/pocketdev/internal/provision"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// watchFirstBoot renders live progress until cloud-init finishes. It returns nil
// on success, or a soft error (timeout / cloud-init error) the caller can note.
func watchFirstBoot(ctx context.Context, p *provision.Provisioner) error {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	final, err := tea.NewProgram(bootModel{ctx: ctx, p: p, sp: sp, start: time.Now()}).Run()
	if err != nil {
		return err
	}
	return final.(bootModel).failed
}

type bootMsg struct {
	status, detail string
	err            error
}
type pollNowMsg struct{}

type bootModel struct {
	ctx            context.Context
	p              *provision.Provisioner
	sp             spinner.Model
	start          time.Time
	status, detail string
	attempts       int
	lastErr        error
	failed         error
}

func (m bootModel) Init() tea.Cmd { return tea.Batch(m.sp.Tick, m.poll()) }

func (m bootModel) poll() tea.Cmd {
	return func() tea.Msg {
		m.p.ResolveHost(m.ctx) // refresh the online tailnet IP via the local CLI
		s, d, e := m.p.BootProgress(m.ctx)
		return bootMsg{status: s, detail: d, err: e}
	}
}

func (m bootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.failed = fmt.Errorf("stopped watching (the box keeps provisioning)")
			return m, tea.Quit
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd
	case bootMsg:
		m.attempts++
		m.lastErr = msg.err
		if msg.detail != "" {
			m.detail = msg.detail
		}
		if msg.err == nil && msg.status != "" {
			m.status = msg.status
			// Match by substring: cloud-init can report "degraded done" / "degraded error".
			switch {
			case strings.Contains(msg.status, "error"):
				m.failed = fmt.Errorf("cloud-init reported an error; SSH in and read /var/log/cloud-init-output.log")
				return m, tea.Quit
			case strings.Contains(msg.status, "done"):
				return m, tea.Quit
			}
		}
		// Bound by wall-clock, not attempt count: a fresh-image apt upgrade + node
		// + agent installs in a distant region can legitimately take a while.
		if time.Since(m.start) > 15*time.Minute {
			m.failed = fmt.Errorf("first-boot still running after %s; it should finish soon — SSH in to check",
				time.Since(m.start).Round(time.Second))
			return m, tea.Quit
		}
		return m, tea.Tick(4*time.Second, func(time.Time) tea.Msg { return pollNowMsg{} })
	case pollNowMsg:
		return m, m.poll()
	}
	return m, nil
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func (m bootModel) View() string {
	el := time.Since(m.start).Round(time.Second)
	user, host := m.p.SSHUserHost()
	line := fmt.Sprintf(" %s Finishing first-boot on the box  %s",
		m.sp.View(), dimStyle.Render("("+el.String()+" elapsed)"))
	line += "\n   " + dimStyle.Render("ssh "+user+"@"+host)

	switch {
	case m.lastErr != nil:
		msg := m.detail // BootProgress puts ssh's stderr here on failure
		if msg == "" {
			msg = m.lastErr.Error()
		}
		line += "  " + dimStyle.Render("· "+clip(msg, 70))
	case m.detail != "":
		line += "  " + dimStyle.Render("· "+clip(m.detail, 70))
	case m.status != "":
		line += "  " + dimStyle.Render("· status: "+m.status)
	default:
		line += "  " + dimStyle.Render("· connecting…")
	}
	return line + "\n"
}
