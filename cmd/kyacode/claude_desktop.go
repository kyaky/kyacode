package main

// cmdClaudeDesktop: the Claude Desktop third-party-inference wiring.
// Unlike the codex desktop route (a registry override + spawn-time
// discrimination), Claude's 3P mode is configured by two files under
// %LOCALAPPDATA%\Claude-3p\configLibrary and applies app-wide at the
// next launch: the whole app - Chat included - runs on the gateway, the
// claude.ai sign-in is bypassed, and there is no per-instance
// coexistence to be had. `on` says so loudly.
//
// The credential never rests in the config: inferenceCredentialHelper
// points the app at kyacode itself, and `claude-desktop-cred` prints
// the token from ~/.kyacode/token at call time (the app's documented
// helper contract: bare token on stdout, exit 0). Unauth and key
// rotation take effect on the helper's next invocation.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"kyacode/internal/claude3p"
	"kyacode/internal/config"
	"kyacode/internal/harness"
	"kyacode/internal/procctl"
)

func cmdClaudeDesktop(args []string, ch channels) error {
	action, rest := args[0], args[1:]
	switch action {
	case "on":
		// The shape gate bounds the rest at one: the optional model pins
		// the picker's default (the config's first entry).
		model := ""
		if len(rest) == 1 {
			model = strings.TrimSpace(rest[0])
		}
		return cmdClaudeDesktopOn(model, ch)
	case "off":
		if err := modellessRefusal(action, rest); err != nil {
			return err
		}
		return cmdClaudeDesktopOff(ch)
	case "status":
		if err := modellessRefusal(action, rest); err != nil {
			return err
		}
		return cmdClaudeDesktopStatus(ch)
	case "open":
		if err := modellessRefusal(action, rest); err != nil {
			return err
		}
		return cmdClaudeDesktopOpen(ch)
	default:
		return fmt.Errorf("unknown claude-desktop action %q (want on, off, status or open)", action)
	}
}

// modellessRefusal is the loud stray-model guard for the actions that
// take no model (the shape gate bounds the argv but cannot know the
// action/model split): a silent ignore would pin nothing and look like
// success.
func modellessRefusal(action string, rest []string) error {
	if len(rest) == 0 {
		return nil
	}
	return fmt.Errorf("claude-desktop %s takes no model argument (got %s) - the optional model belongs to `on`", action, strings.Join(rest, " "))
}

// cmdClaudeDesktopOn writes the 3P configuration: the model list is
// fetched live from the gateway (the picker is load-bearing - without
// explicit entries, Claude-recognizable-id discovery would hide every
// glm/qwen model), the helper is the running kyacode binary, and the
// app-wide semantics are printed loudly before anything is launched.
// The optional model argument pins the picker's DEFAULT (the config's
// first entry) to the operator's choice; the rest of the chat catalog
// still lists, because the picker's rows are choices, not defaults. An
// id the fetched chat list does not serve is refused - a default naming
// an unserved id would boot the app into dead calls.
func cmdClaudeDesktopOn(model string, ch channels) error {
	ep, err := resolveEndpoint()
	if err != nil {
		return err
	}
	models, err := ep.Base().Models()
	if err != nil {
		return fmt.Errorf("fetch the gateway model list: %w (check the gateway with `kyacode doctor`)", err)
	}
	var chat []string
	for _, m := range models {
		if harness.GatewayChatModel(m) {
			chat = append(chat, m)
		}
	}
	if model != "" {
		idx := slices.Index(chat, model)
		if idx < 0 {
			return fmt.Errorf("claude-desktop on: the gateway's chat list does not serve %q - check `kyacode models`", model)
		}
		rest := slices.Delete(slices.Clone(chat), idx, idx+1)
		chat = append([]string{model}, rest...)
	}
	helper, err := os.Executable()
	if err != nil {
		return err
	}
	id, err := claude3p.Apply(chat, ep.BaseURL, helper)
	if err != nil {
		return err
	}
	fmt.Fprintln(ch.stdout, "claude desktop wired: the app switches ENTIRELY to the gateway at its next launch")
	fmt.Fprintf(ch.stdout, "  applied configuration: %s.json (model picker carries %d gateway models, effort up to high)\n", id, len(chat))
	if len(chat) > 0 {
		if model != "" {
			fmt.Fprintf(ch.stdout, "  picker default: %s (your model argument leads the list)\n", chat[0])
		} else {
			fmt.Fprintf(ch.stdout, "  picker default: %s (the alphabetically first chat id; pass a model to `on` to pin your own)\n", chat[0])
		}
	}
	fmt.Fprintf(ch.stdout, "  credential: the app calls this kyacode binary as its credential helper - the key lives only in ~/.kyacode/token\n")
	fmt.Fprintln(ch.stdout, "  semantics: the claude.ai sign-in is bypassed, chat runs on gateway models, cloud environments and Computer Use are unavailable")
	fmt.Fprintln(ch.stdout, "  launch with: kyacode claude-desktop open")
	fmt.Fprintln(ch.stdout, "  revert with: kyacode claude-desktop off (the app returns to its sign-in screen)")
	return nil
}

func cmdClaudeDesktopOff(ch channels) error {
	if err := claude3p.Remove(); err != nil {
		return err
	}
	fmt.Fprintln(ch.stdout, "claude desktop wiring removed (the app returns to its sign-in screen at the next launch)")
	return nil
}

// cmdClaudeDesktopStatus reports the wiring layers, including whether
// the applied configuration is still ours (the in-app configuration
// window can flip appliedId out from under us).
func cmdClaudeDesktopStatus(ch channels) error {
	state, applied, err := claude3p.ReadState()
	if err != nil {
		return err
	}
	switch state {
	case claude3p.StateAbsent:
		fmt.Fprintln(ch.stdout, "config:   absent (the app runs first-party)")
	case claude3p.StateOurs:
		fmt.Fprintf(ch.stdout, "config:   kyacode's configuration is applied (%s.json)\n", applied)
	case claude3p.StateOursUnapplied:
		fmt.Fprintf(ch.stdout, "config:   kyacode's entry exists but another configuration is applied (%s.json) - re-run `claude-desktop on` to re-apply\n", applied)
	case claude3p.StateForeign:
		fmt.Fprintf(ch.stdout, "config:   a non-kyacode configuration is applied (%s.json) - kyacode will not touch it\n", applied)
	}
	helper, err := os.Executable()
	if err == nil {
		fmt.Fprintf(ch.stdout, "helper:   %s claude-desktop-cred\n", helper)
	}
	return nil
}

// cmdClaudeDesktopOpen launches the desktop app (which reads the 3P
// config at launch). The app outlives the launcher.
func cmdClaudeDesktopOpen(ch channels) error {
	state, _, err := claude3p.ReadState()
	if err != nil {
		return err
	}
	if state != claude3p.StateOurs {
		return fmt.Errorf("the claude desktop wiring is not applied - run `kyacode claude-desktop on` first")
	}
	exe, err := claudeDesktopAppExe()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe)
	procctl.ConfigureGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch %s: %w", exe, err)
	}
	fmt.Fprintf(ch.stdout, "claude desktop launched (3P mode; the Code tab runs on the gateway)\n")
	return nil
}

// cmdClaudeDesktopCred is the credential helper the app invokes: the
// gateway token, bare, on stdout, nothing else - the helper contract
// rejects any other output shape. Errors ride stderr through the normal
// command failure path.
func cmdClaudeDesktopCred(ch channels) error {
	tok, err := config.Token()
	if err != nil {
		return err
	}
	fmt.Fprint(ch.stdout, tok)
	return nil
}

// claudeDesktopAppExe resolves the installed desktop app's executable
// through the package data Windows exposes via PowerShell (the
// manifest's Application entry declares app\\Claude.exe).
func claudeDesktopAppExe() (string, error) {
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		"(Get-AppxPackage Claude).InstallLocation").Output()
	if err != nil {
		return "", fmt.Errorf("locate the Claude desktop app (Get-AppxPackage): %w", err)
	}
	loc := strings.TrimSpace(string(out))
	if loc == "" {
		return "", fmt.Errorf("the Claude desktop app is not installed (Get-AppxPackage Claude returned nothing)")
	}
	exe := filepath.Join(loc, "app", "Claude.exe")
	if fi, err := os.Stat(exe); err == nil && !fi.IsDir() {
		return exe, nil
	}
	return "", fmt.Errorf("no Claude.exe under %s", loc)
}
