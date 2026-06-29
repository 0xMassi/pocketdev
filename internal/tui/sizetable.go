package tui

import (
	"fmt"
	"strings"

	"github.com/0xMassi/pocketdev/internal/hetzner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/huh"
)

var sizeColumns = []table.Column{
	{Title: "TYPE", Width: 8},
	{Title: "REGION", Width: 26},
	{Title: "vCPU", Width: 5},
	{Title: "RAM", Width: 7},
	{Title: "DISK", Width: 8},
	{Title: "PRICE", Width: 15},
}

// sizeRows turns size options into table rows (value = "type|region").
func sizeRows(opts []hetzner.SizeOption) []pickRow {
	rows := make([]pickRow, 0, len(opts))
	for _, o := range opts {
		rows = append(rows, pickRow{
			cells: []string{
				o.Type, regionLabel(o), fmt.Sprintf("%d", o.Cores),
				fmt.Sprintf("%gGB", o.MemoryGB), fmt.Sprintf("%dGB", o.DiskGB), priceLabel(o),
			},
			value:  o.Type + "|" + o.Region,
			search: strings.ToLower(o.Type + " " + regionLabel(o)),
		})
	}
	return rows
}

// pickSize shows the size table and returns the chosen "type|region".
func pickSize(opts []hetzner.SizeOption, preselect string) (key string, back bool, err error) {
	v, back, cancelled, err := tablePick(
		"Size",
		"Net /mo (ex VAT). type to filter · ↑↓ move · enter select · shift+tab back · ctrl+c quit",
		sizeColumns, sizeRows(opts), preselect)
	if err != nil {
		return "", false, err
	}
	if cancelled {
		return "", false, huh.ErrUserAborted
	}
	return v, back, nil
}
