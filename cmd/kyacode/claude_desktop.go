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
	"strings"

	"kyacode/internal/claude3p"
	"kyacode/internal/config"
	"kyacode/internal/harness"
	"kyacode/internal/procctl"
)

func cmdClaudeDesktop(args []string, ch channels) error {
	switch args[0] {
	case "on":
		return cmdClaudeDesktopOn(ch)
	case "off":
		return cmdClaudeDesktopOff(ch)
	case "status":
		return cmdClaudeDesktopStatus(ch)
	case "open":
		return cmdClaudeDesktopOpen(ch)
	default:
		return fmt.Errorf("unknown claude-desktop action %q (want on, off, status or open)", args[0])
	}
}

// cmdClaudeDesktopOn writes the 3P configuration: the model list is
// fetched live from the gateway (the picker is load-bearing - without
// explicit entries, Claude-recognizable-id discovery would hide every
// glm/qwen model), the helper is the running kyacode binary, and the
// app-wide semantics are printed loudly before anything is launched.
func cmdClaudeDesktopOn(ch channels) error {
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
