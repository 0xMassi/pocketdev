// A Bubble Tea file browser with an always-on search bar. Start typing to filter
// the current directory; arrows move; enter opens a folder or picks a file; the
// top "[ use this folder ]" row selects the current directory.
package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// pickLocalPath browses from start and returns the chosen folder or file path.
func pickLocalPath(start string) (path string, back bool, err error) {
	final, err := tea.NewProgram(newBrowse(start)).Run()
	if err != nil {
		return "", false, err
	}
	m := final.(browseModel)
	if m.cancelled {
		return "", false, huh.ErrUserAborted
	}
	return m.path, m.back, nil
}

type entry struct {
	name   string
	dir    bool
	up     bool // the ".." parent entry
	useCwd bool // the synthetic "use this folder" entry
	full   string
}

type browseModel struct {
	cwd        string
	all        []entry
	view       []entry
	tbl        table.Model
	filter     textinput.Model
	showHidden bool
	errMsg     string

	path      string
	back      bool
	cancelled bool
}

func newBrowse(start string) browseModel {
	t := table.New(
		table.WithColumns([]table.Column{{Title: "NAME", Width: 60}}),
		table.WithFocused(true), table.WithHeight(14))
	st := table.DefaultStyles()
	st.Header = st.Header.BorderStyle(lipgloss.NormalBorder()).BorderBottom(true).Bold(true).Foreground(lipgloss.Color("212"))
	st.Selected = st.Selected.Foreground(lipgloss.Color("231")).Background(lipgloss.Color("212")).Bold(true)
	t.SetStyles(st)

	fi := textinput.New()
	fi.Prompt = "search ❯ "
	fi.Placeholder = "type to filter"
	fi.Focus()

	m := browseModel{cwd: start, tbl: t, filter: fi}
	m.load(start)
	return m
}

func (m *browseModel) load(dir string) {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	items, err := os.ReadDir(dir)
	if err != nil {
		m.errMsg = "cannot open " + dir + " (" + err.Error() + ")"
		return // keep the current listing
	}
	m.errMsg = ""
	m.cwd = dir
	m.filter.SetValue("")

	var dirs, files []entry
	for _, it := range items {
		if !m.showHidden && strings.HasPrefix(it.Name(), ".") {
			continue
		}
		e := entry{name: it.Name(), dir: it.IsDir(), full: filepath.Join(dir, it.Name())}
		if e.dir {
			dirs = append(dirs, e)
		} else {
			files = append(files, e)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].name < dirs[j].name })
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	m.all = m.all[:0]
	m.all = append(m.all, entry{name: "[ use this folder ]", useCwd: true})
	if parent := filepath.Dir(dir); parent != dir {
		m.all = append(m.all, entry{name: "..", dir: true, up: true, full: parent})
	}
	m.all = append(m.all, dirs...)
	m.all = append(m.all, files...)
	m.rebuild()
}

func (m *browseModel) rebuild() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	m.view = m.view[:0]
	var rows []table.Row
	for _, e := range m.all {
		pinned := e.useCwd || e.up
		if !pinned && q != "" && !strings.Contains(strings.ToLower(e.name), q) {
			continue
		}
		m.view = append(m.view, e)
		label := e.name
		if e.dir && !e.up {
			label += "/"
		}
		rows = append(rows, table.Row{label})
	}
	m.tbl.SetRows(rows)
	m.tbl.SetCursor(0)
}

func (m browseModel) Init() tea.Cmd { return textinput.Blink }

func (m browseModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		return m, cmd
	}
	switch s := key.String(); {
	case s == "ctrl+c":
		m.cancelled = true
		return m, tea.Quit
	case s == "shift+tab":
		m.back = true
		return m, tea.Quit
	case s == "esc":
		if m.filter.Value() != "" {
			m.filter.SetValue("")
			m.rebuild()
			return m, nil
		}
		m.back = true
		return m, tea.Quit
	case s == "ctrl+a": // toggle hidden files
		m.showHidden = !m.showHidden
		m.load(m.cwd)
		return m, nil
	case s == "enter":
		if len(m.view) == 0 {
			return m, nil
		}
		e := m.view[m.tbl.Cursor()]
		switch {
		case e.useCwd:
			m.path = m.cwd
			return m, tea.Quit
		case e.dir: // descend or go up
			m.load(e.full)
			return m, nil
		default: // pick a file
			m.path = e.full
			return m, tea.Quit
		}
	case isNav(s):
		var cmd tea.Cmd
		m.tbl, cmd = m.tbl.Update(msg)
		return m, cmd
	default:
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.rebuild()
		return m, cmd
	}
}

func (m browseModel) View() string {
	var b strings.Builder
	b.WriteString(stepStyle.Render("▸ Local folder") + "\n")
	b.WriteString(dimStyle.Render(m.cwd) + "\n")
	if m.errMsg != "" {
		b.WriteString(errStyle.Render("  "+m.errMsg) + "\n")
	}
	b.WriteString(dimStyle.Render("type to filter · ↑↓ move · enter open/pick · ctrl+a hidden · shift+tab back · ctrl+c quit") + "\n")
	b.WriteString(m.filter.View() + "\n")
	b.WriteString(m.tbl.View() + "\n")
	return b.String()
}
