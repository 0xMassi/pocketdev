package tui

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/huh"
)

type ghRepo struct {
	NameWithOwner string `json:"nameWithOwner"`
	Description   string `json:"description"`
	IsPrivate     bool   `json:"isPrivate"`
}

// listRepos returns the signed-in user's repos via the local gh CLI. It errors
// if gh is missing or not authenticated, so the caller can fall back to input.
func listRepos(ctx context.Context) ([]ghRepo, error) {
	bin, err := exec.LookPath("gh")
	if err != nil {
		return nil, err
	}
	if err := exec.CommandContext(ctx, bin, "auth", "status").Run(); err != nil {
		return nil, err
	}
	out, err := exec.CommandContext(ctx, bin, "repo", "list",
		"--json", "nameWithOwner,description,isPrivate", "--limit", "500").Output()
	if err != nil {
		return nil, err
	}
	var repos []ghRepo
	if err := json.Unmarshal(out, &repos); err != nil {
		return nil, err
	}
	return repos, nil
}

var repoColumns = []table.Column{
	{Title: "REPO", Width: 38},
	{Title: "", Width: 8},
	{Title: "DESCRIPTION", Width: 50},
}

// manualRepo is the sentinel value returned when the user picks "type manually".
const manualRepo = "\x00manual"

// pickRepo shows a searchable list of repos and returns the chosen "owner/name",
// or manualRepo if the user chose to type one in.
func pickRepo(repos []ghRepo, preselect string) (name string, back bool, err error) {
	rows := make([]pickRow, 0, len(repos)+1)
	rows = append(rows, pickRow{
		cells:  []string{"✎ Enter a repo manually…", "", ""},
		value:  manualRepo,
		search: "manual enter type other repo",
		pinned: true, // always reachable, even while filtering
	})
	for _, r := range repos {
		vis := "public"
		if r.IsPrivate {
			vis = "private"
		}
		rows = append(rows, pickRow{
			cells:  []string{r.NameWithOwner, vis, r.Description},
			value:  r.NameWithOwner,
			search: strings.ToLower(r.NameWithOwner + " " + r.Description),
		})
	}
	v, back, cancelled, err := tablePick(
		"GitHub repo",
		"type to filter · ↑↓ move · enter select · shift+tab back · ctrl+c quit",
		repoColumns, rows, preselect)
	if err != nil {
		return "", false, err
	}
	if cancelled {
		return "", false, huh.ErrUserAborted
	}
	return v, back, nil
}
