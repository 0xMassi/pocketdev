// Package agents is the registry of supported AI coding CLIs.
//
// It is the single source of truth used to (1) detect which CLI you already use
// on your laptop, (2) generate the unattended install that cloud-init runs on the
// box, and (3) generate the on-box `pocketdev` helper that authenticates each CLI.
//
// Install/login facts verified 2026-06-29 against each tool's official docs.
package agents

import "strings"

// Agent describes one AI coding CLI.
type Agent struct {
	Key       string   // stable id, e.g. "claude"
	Name      string   // display name + which subscription it reuses
	Bins      []string // binary name(s) used to detect it on the laptop / box
	SysDeps   []string // apt deps installed as root before the user install: node|pipx|keyring
	Install   string   // shell command to install it as the non-root "dev" user
	LoginCmd  string   // command the on-box helper runs to authenticate
	LoginHint string   // one-line human hint (kept apostrophe-free for safe shell echo)
	APIKeyEnv string   // env var for the API-key fallback
	Note      string   // caveat surfaced in the README / TUI
}

// All agents, ordered by how well they fit "reuse my subscription from a phone".
var All = []Agent{
	{
		Key:       "claude",
		Name:      "Claude Code (Claude Pro/Max)",
		Bins:      []string{"claude"},
		Install:   "curl -fsSL https://claude.ai/install.sh | bash",
		LoginCmd:  "claude",
		LoginHint: "pick the paste-code login: open the URL, paste the code back",
		APIKeyEnv: "ANTHROPIC_API_KEY",
		Note:      "Subscription login works over SSH (paste-code). Do not set ANTHROPIC_API_KEY if you want subscription billing; it overrides the login.",
	},
	{
		Key:       "codex",
		Name:      "OpenAI Codex CLI (ChatGPT Plus/Pro/Business)",
		Bins:      []string{"codex"},
		Install:   "curl -fsSL https://chatgpt.com/codex/install.sh | sh",
		LoginCmd:  "codex login --device-auth",
		LoginHint: "device-code flow: open the URL, enter the code, then run codex",
		APIKeyEnv: "OPENAI_API_KEY",
		Note:      "Use device-auth for a clean login over SSH; no port-forward needed.",
	},
	{
		Key:       "opencode",
		Name:      "opencode (Claude Pro/Max, GitHub Copilot, ChatGPT)",
		Bins:      []string{"opencode"},
		Install:   "curl -fsSL https://opencode.ai/install | bash",
		LoginCmd:  "opencode auth login",
		LoginHint: "Claude Pro/Max uses paste-code, GitHub Copilot uses a device flow",
		APIKeyEnv: "ANTHROPIC_API_KEY",
		Note:      "Claude Pro/Max and Copilot complete over SSH. ChatGPT OAuth needs a port-forward.",
	},
	{
		Key:       "cursor",
		Name:      "Cursor CLI (Cursor Pro/Business)",
		Bins:      []string{"cursor-agent"},
		SysDeps:   []string{"keyring"},
		Install:   "curl https://cursor.com/install -fsS | bash",
		LoginCmd:  "NO_OPEN_BROWSER=1 cursor-agent login",
		LoginHint: "open the printed URL on your phone and approve",
		APIKeyEnv: "CURSOR_API_KEY",
		Note:      "Needs a keyring on the box (installed for you). If token storage fails, use CURSOR_API_KEY.",
	},
	{
		Key:       "gemini",
		Name:      "Google Gemini CLI (Google account / AI Pro/Ultra)",
		Bins:      []string{"gemini"},
		SysDeps:   []string{"node"},
		Install:   `npm config set prefix "$HOME/.npm-global" && npm install -g @google/gemini-cli`,
		LoginCmd:  "gemini",
		LoginHint: "choose Sign in with Google (headless OAuth is fiddly; API key is steadier)",
		APIKeyEnv: "GEMINI_API_KEY",
		Note:      "Headless Google OAuth binds a dynamic port and can be brittle; GEMINI_API_KEY is the reliable path.",
	},
	{
		Key:       "grok",
		Name:      "Grok Build CLI (SuperGrok / X Premium+)",
		Bins:      []string{"grok"},
		Install:   "curl -fsSL https://x.ai/cli/install.sh | bash",
		LoginCmd:  "grok",
		LoginHint: "browser OAuth on first run; if inference 403s, use the API key",
		APIKeyEnv: "GROK_CODE_XAI_API_KEY",
		Note:      "Install + headless OAuth are not fully verified upstream; GROK_CODE_XAI_API_KEY is the dependable path.",
	},
	{
		Key:       "aider",
		Name:      "Aider (API key only, no subscription login)",
		Bins:      []string{"aider"},
		SysDeps:   []string{"pipx"},
		Install:   "pipx install aider-chat",
		LoginCmd:  "aider",
		LoginHint: "set an API key first; aider has no subscription login",
		APIKeyEnv: "ANTHROPIC_API_KEY",
		Note:      "Aider talks to provider APIs (per-token billing). Chat subscriptions cannot be reused.",
	},
}

// ByKey returns the agent with the given key.
func ByKey(key string) (Agent, bool) {
	for _, a := range All {
		if a.Key == key {
			return a, true
		}
	}
	return Agent{}, false
}

// Keys returns every known agent key.
func Keys() []string {
	ks := make([]string, len(All))
	for i, a := range All {
		ks[i] = a.Key
	}
	return ks
}

// Resolve maps keys to agents, erroring on the first unknown key.
func Resolve(keys []string) ([]Agent, error) {
	out := make([]Agent, 0, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		a, ok := ByKey(k)
		if !ok {
			return nil, &UnknownAgentError{Key: k}
		}
		out = append(out, a)
	}
	return out, nil
}

// UnknownAgentError is returned by Resolve for an unrecognised key.
type UnknownAgentError struct{ Key string }

func (e *UnknownAgentError) Error() string {
	return "unknown agent: " + e.Key + " (choices: " + strings.Join(Keys(), ", ") + ")"
}
