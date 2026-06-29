package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/0xMassi/pocketdev/internal/hetzner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
)

func sample() []hetzner.SizeOption {
	return []hetzner.SizeOption{
		{Type: "cax21", Region: "nbg1", City: "Nuremberg", Country: "DE", Cores: 4, MemoryGB: 8, DiskGB: 80, Monthly: 7.99, Currency: "EUR", Group: hetzner.GroupShared, Class: hetzner.ClassCostOptimized, ArchLabel: hetzner.ArchArm},
		{Type: "cax21", Region: "fsn1", City: "Falkenstein", Country: "DE", Cores: 4, MemoryGB: 8, DiskGB: 80, Monthly: 7.99, Currency: "EUR", Group: hetzner.GroupShared, Class: hetzner.ClassCostOptimized, ArchLabel: hetzner.ArchArm},
		{Type: "cpx41", Region: "ash", City: "Ashburn", Country: "US", Cores: 8, MemoryGB: 16, DiskGB: 240, Monthly: 31.99, Currency: "EUR", Group: hetzner.GroupShared, Class: hetzner.ClassRegular, ArchLabel: hetzner.ArchX86AMD},
	}
}

func TestSplitSel(t *testing.T) {
	if st, rg := splitSel("cax21|nbg1"); st != "cax21" || rg != "nbg1" {
		t.Fatalf("got %q/%q", st, rg)
	}
	if st, rg := splitSel("solo"); st != "solo" || rg != "" {
		t.Fatalf("no-pipe: got %q/%q", st, rg)
	}
}

func TestDefaultSel(t *testing.T) {
	opts := sample()
	if got := defaultSel(opts, "cax21", "fsn1"); got != "cax21|fsn1" {
		t.Errorf("saved selection not honored: %q", got)
	}
	if got := defaultSel(opts, "ccx99", "xxx"); got != "cax21|nbg1" {
		t.Errorf("fallback should be the first option: %q", got)
	}
	if got := defaultSel(nil, "a", "b"); got != "" {
		t.Errorf("empty: %q", got)
	}
}

func TestCountryFlag(t *testing.T) {
	for cc, want := range map[string]string{"DE": "🇩🇪", "US": "🇺🇸", "SG": "🇸🇬"} {
		if got := countryFlag(cc); got != want {
			t.Errorf("countryFlag(%q) = %q, want %q", cc, got, want)
		}
	}
	for _, bad := range []string{"", "D", "DEU", "1A"} {
		if got := countryFlag(bad); got != "" {
			t.Errorf("countryFlag(%q) = %q, want empty", bad, got)
		}
	}
}

func TestRegionAndPriceLabels(t *testing.T) {
	o := sample()[0]
	if got := regionLabel(o); got != "🇩🇪 Nuremberg (nbg1)" {
		t.Errorf("regionLabel = %q", got)
	}
	if got := priceLabel(o); got != "EUR 7.99/mo" {
		t.Errorf("priceLabel = %q", got)
	}
	if got := priceLabel(hetzner.SizeOption{}); got != "n/a" {
		t.Errorf("priceLabel(zero) = %q, want n/a", got)
	}
}

func TestSizeRows(t *testing.T) {
	rows := sizeRows(sample())
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	if rows[0].value != "cax21|nbg1" {
		t.Errorf("row value = %q, want cax21|nbg1", rows[0].value)
	}
	if rows[0].cells[1] != "🇩🇪 Nuremberg (nbg1)" {
		t.Errorf("region cell = %q", rows[0].cells[1])
	}
}

func TestPickerFilterAndPreselect(t *testing.T) {
	m := newPicker("Size", "", sizeColumns, sizeRows(sample()), "cax21|fsn1")
	if len(m.view) != 3 {
		t.Fatalf("unfiltered view = %d, want 3", len(m.view))
	}
	if m.tbl.Cursor() != 1 { // fsn1 is the second row -> preselected
		t.Errorf("preselect cursor = %d, want 1", m.tbl.Cursor())
	}
	m.filter.SetValue("ash")
	m.rebuild()
	if len(m.view) != 1 || m.view[0].value != "cpx41|ash" {
		t.Errorf("filter 'ash' -> %d rows (want 1: cpx41|ash)", len(m.view))
	}
}

func TestPickerEnterEmptyViewIsNoOp(t *testing.T) {
	m := newPicker("Size", "", sizeColumns, sizeRows(sample()), "")
	m.filter.SetValue("zzzzz")
	m.rebuild()
	if len(m.view) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(m.view))
	}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if v := out.(pickerModel).value; v != "" {
		t.Errorf("enter on empty view set value=%q, want empty", v)
	}
}

func TestPickerTypeToFilter(t *testing.T) {
	m := newPicker("Size", "", sizeColumns, sizeRows(sample()), "")
	for _, r := range "cpx" { // type without pressing '/'
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = out.(pickerModel)
	}
	if m.filter.Value() != "cpx" {
		t.Fatalf("filter = %q, want cpx", m.filter.Value())
	}
	if len(m.view) != 1 || m.view[0].value != "cpx41|ash" {
		t.Errorf("typing 'cpx' -> %d rows, want 1 (cpx41|ash)", len(m.view))
	}
}

func TestPickerPinnedRowSurvivesFilter(t *testing.T) {
	cols := []table.Column{{Title: "X", Width: 20}}
	rows := []pickRow{
		{cells: []string{"manual"}, value: "__m", search: "manual enter", pinned: true},
		{cells: []string{"alpha"}, value: "a", search: "alpha"},
	}
	m := newPicker("x", "", cols, rows, "")
	for _, r := range "zzz" { // matches nothing
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = out.(pickerModel)
	}
	if len(m.view) != 1 || m.view[0].value != "__m" {
		t.Errorf("pinned row not preserved under a no-match filter: view=%d", len(m.view))
	}
}

func TestBrowseHiddenToggle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "visible"), 0o755); err != nil {
		t.Fatal(err)
	}
	has := func(m browseModel, name string) bool {
		for _, e := range m.all {
			if e.name == name {
				return true
			}
		}
		return false
	}
	m := newBrowse(dir)
	if has(m, ".secret") {
		t.Error(".secret should be hidden by default")
	}
	if !has(m, "visible") {
		t.Error("visible/ should be listed")
	}
	m.showHidden = true
	m.load(dir)
	if !has(m, ".secret") {
		t.Error(".secret should appear once showHidden is on")
	}
}

func TestDistinctHierarchy(t *testing.T) {
	all := sample()
	if g := distinctGroups(all); len(g) != 1 || g[0] != hetzner.GroupShared {
		t.Errorf("groups = %v", g)
	}
	// Shared has both Cost-Optimized (cax) and Regular (cpx) in the sample.
	classes := distinctClasses(all, hetzner.GroupShared)
	if len(classes) != 2 || classes[0] != hetzner.ClassCostOptimized || classes[1] != hetzner.ClassRegular {
		t.Errorf("classes = %v", classes)
	}
	leaf := filterLeaf(all, hetzner.GroupShared, hetzner.ClassCostOptimized, hetzner.ArchArm)
	if len(leaf) != 2 {
		t.Errorf("cost-optimized arm leaf size = %d, want 2", len(leaf))
	}
}
