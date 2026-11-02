package main

// cmdDesktop: the ChatGPT/Codex desktop-app wiring surface. The desktop
// route is the shim + registry design from the 2026-11-02 research
// verdict: MSIX activation rebuilds the app's process environment from
// the registry, so CODEX_CLI_PATH is the only channel that reaches the
// host's backend selection, and the shim's spawn-time ancestry check
// (the electron-profile marker) keeps the user's everyday signed-in
// instance on vanilla behavior while kyacode-launched instances run on
// the gateway home.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"kyacode/internal/procctl"
	"kyacode/internal/shim"
)

func cmdDesktop(args []string, ch channels) error {
	switch args[0] {
	case "on":
		return cmdDesktopOn(ch)
	case "off":
		return cmdDesktopOff(ch)
	case "status":
		return cmdDesktopStatus(ch)
	case "open":
		return cmdDesktopOpen(ch)
	default:
		return fmt.Errorf("unknown desktop action %q (want on, off, status or open)", args[0])
	}
}

// cmdDesktopOn installs the shim and registers the CODEX_CLI_PATH
// override. The gateway home is NOT required here: wiring may precede
// the first launch, and the shim fails closed on gateway spawns until
// the home exists.
func cmdDesktopOn(ch channels) error {
	if err := shim.Install(); err != nil {
		return err
	}
	if err := shim.SetEnabled(true); err != nil {
		return err
	}
	if err := shim.DesktopRegistryOn(); err != nil {
		return err
	}
	dst, _ := shim.ExePath()
	fmt.Fprintln(ch.stdout, "desktop wired: the desktop app's engine now spawns through the kyacode shim")
	fmt.Fprintf(ch.stdout, "  override: CODEX_CLI_PATH=%s\n", dst)
	fmt.Fprintln(ch.stdout, "  everyday desktop instances keep their vanilla engine; kyacode-launched ones run on the gateway home")
	fmt.Fprintln(ch.stdout, "  launch a gateway instance with: kyacode desktop open")
	fmt.Fprintln(ch.stdout, "  revoke with: kyacode desktop off")
	return nil
}

func cmdDesktopOff(ch channels) error {
	if err := shim.DesktopRegistryOff(); err != nil {
		return err
	}
	fmt.Fprintln(ch.stdout, "desktop wiring removed (CODEX_CLI_PATH restored to its prior state)")
	return nil
}

// cmdDesktopStatus reports every wiring fact the route depends on.
func cmdDesktopStatus(ch channels) error {
	dst, err := shim.ExePath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		fmt.Fprintf(ch.stdout, "shim:     installed at %s\n", dst)
	} else {
		fmt.Fprintf(ch.stdout, "shim:     missing (run `kyacode desktop on`)\n")
	}
	enabled := false
	if p, err := shim.EnableFlagPath(); err == nil {
		if _, err := os.Stat(p); err == nil {
			enabled = true
		}
	}
	fmt.Fprintf(ch.stdout, "engine flag: %v\n", enabled)
	cur, ours, err := shim.DesktopCLIPATHState()
	if err != nil {
		return err
	}
	switch {
	case cur == "":
		fmt.Fprintln(ch.stdout, "registry: CODEX_CLI_PATH absent (desktop app runs its own engine)")
	case ours:
		fmt.Fprintln(ch.stdout, "registry: CODEX_CLI_PATH points at the kyacode shim")
	default:
		fmt.Fprintf(ch.stdout, "registry: CODEX_CLI_PATH is foreign (%s) - not kyacode's\n", cur)
	}
	home, err := shim.HomeDir()
	if err != nil {
		return err
	}
	if _, err := os.Stat(home); err == nil {
		fmt.Fprintf(ch.stdout, "home:     %s\n", home)
	} else {
		fmt.Fprintln(ch.stdout, "home:     missing (run `kyacode launch codex-gui <model>` to materialise it)")
	}
	return nil
}

// cmdDesktopOpen launches a gateway desktop instance: the app exe with
// the dedicated electron profile whose marker the shim's ancestry check
// reads. Requires the wiring (`desktop on`) and the GUI home (a
// `kyacode launch codex-gui <model>` run).
func cmdDesktopOpen(ch channels) error {
	cur, ours, err := shim.DesktopCLIPATHState()
	if err != nil {
		return err
	}
	if cur == "" || !ours {
		return fmt.Errorf("the desktop wiring is off - run `kyacode desktop on` first (current CODEX_CLI_PATH: %s)", displayValue(cur))
	}
	home, err := shim.HomeDir()
	if err != nil {
		return err
	}
	if _, err := os.Stat(home); err != nil {
		return fmt.Errorf("no GUI home yet - run `kyacode launch codex-gui <model>` first")
	}
	exe, err := desktopAppExe()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "--user-data-dir="+shim.ElectronProfileDirValue())
	procctl.ConfigureGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch %s: %w", exe, err)
	}
	fmt.Fprintf(ch.stdout, "desktop app launched on the gateway home (engine spawns route through the shim)\n")
	return nil
}

// displayValue renders an empty registry value readably.
func displayValue(v string) string {
	if strings.TrimSpace(v) == "" {
		return "<absent>"
	}
	return v
}

// desktopAppExe resolves the installed desktop app's real executable
// through the package manifest data Windows exposes via PowerShell. The
// Store wrapper's own launcher drops arguments, so the app directory's
// ChatGPT.exe is preferred (the chatgpt-switcher finding).
func desktopAppExe() (string, error) {
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		"(Get-AppxPackage OpenAI.Codex).InstallLocation").Output()
	if err != nil {
		return "", fmt.Errorf("locate the desktop app (Get-AppxPackage): %w", err)
	}
	loc := strings.TrimSpace(string(out))
	if loc == "" {
		return "", fmt.Errorf("the Codex desktop app is not installed (Get-AppxPackage OpenAI.Codex returned nothing)")
	}
	for _, candidate := range []string{
		filepath.Join(loc, "app", "ChatGPT.exe"),
		filepath.Join(loc, "ChatGPT.exe"),
	} {
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no ChatGPT.exe under %s", loc)
}
