// A generic Bubble Tea table picker with an always-on search bar: start typing
// to filter, arrows to move, enter to select, shift+tab to go back, ctrl+c quit.
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type pickRow struct {
	cells  []string // one per column
	value  string   // returned when chosen
	search string   // lowercased haystack for the filter
	pinned bool     // always shown, even when filtered out (e.g. "enter manually")
}

// tablePick runs the picker and returns the chosen value, whether the user asked
// to go back, and whether they cancelled (ctrl-c).
func tablePick(title, help string, cols []table.Column, rows []pickRow, preselect string) (value string, back, cancelled bool, err error) {
	final, err := tea.NewProgram(newPicker(title, help, cols, rows, preselect)).Run()
	if err != nil {
		return "", false, false, err
	}
	m := final.(pickerModel)
	return m.value, m.back, m.cancelled, nil
}

type pickerModel struct {
	title, help string
	all         []pickRow
	view        []pickRow
	tbl         table.Model
	filter      textinput.Model

	value     string
	back      bool
	cancelled bool
}

func newPicker(title, help string, cols []table.Column, rows []pickRow, preselect string) pickerModel {
	t := table.New(table.WithColumns(cols), table.WithFocused(true), table.WithHeight(12))
	st := table.DefaultStyles()
	st.Header = st.Header.BorderStyle(lipgloss.NormalBorder()).BorderBottom(true).Bold(true).Foreground(lipgloss.Color("212"))
	st.Selected = st.Selected.Foreground(lipgloss.Color("231")).Background(lipgloss.Color("212")).Bold(true)
	t.SetStyles(st)

	fi := textinput.New()
	fi.Prompt = "search ❯ "
	fi.Placeholder = "type to filter"
	fi.Focus()

	m := pickerModel{title: title, help: help, all: rows, tbl: t, filter: fi}
	m.rebuild()
	for i, r := range m.view {
		if r.value == preselect {
			m.tbl.SetCursor(i)
			break
		}
	}
	return m
}

func (m *pickerModel) rebuild() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	m.view = m.view[:0]
	var rows []table.Row
	for _, r := range m.all {
		if !r.pinned && q != "" && !strings.Contains(r.search, q) {
			continue
		}
		m.view = append(m.view, r)
		rows = append(rows, table.Row(r.cells))
	}
	m.tbl.SetRows(rows)
	m.tbl.SetCursor(0)
}

func (m pickerModel) Init() tea.Cmd { return textinput.Blink }

// navKeys are forwarded to the table; everything else types into the search bar.
func isNav(s string) bool {
	switch s {
	case "up", "down", "pgup", "pgdown", "page up", "page down", "home", "end", "ctrl+n", "ctrl+p":
		return true
	}
	return false
}

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg) // keep the cursor blinking
		return m, cmd
	}
	switch s := key.String(); {
	case s == "ctrl+c":
		m.cancelled = true
		return m, tea.Quit
	case s == "enter":
		if len(m.view) == 0 {
			return m, nil
		}
		m.value = m.view[m.tbl.Cursor()].value
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

func (m pickerModel) View() string {
	var b strings.Builder
	b.WriteString(stepStyle.Render("▸ "+m.title) + "\n")
	b.WriteString(dimStyle.Render(m.help) + "\n")
	b.WriteString(m.filter.View() + "\n")
	b.WriteString(m.tbl.View() + "\n")
	return b.String()
}
