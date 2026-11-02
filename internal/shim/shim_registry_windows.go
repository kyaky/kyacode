//go:build windows

package shim

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// The desktop wiring's registry half: CODEX_CLI_PATH in
// HKCU\Environment, delivered exactly, never overwritten blindly. The
// MSIX activation rebuilds the app's process environment from the
// registry (the discovery that killed every in-session injection
// route), so the registry is the only delivery channel - and the only
// one the product may touch. The contract:
//
//   - `on` writes our shim path ONLY when the value is absent; a foreign
//     value is refused loudly, never clobbered.
//   - `off` restores the recorded prior state (absent or a real prior
//     value) ONLY while the current value is still ours; a foreign
//     current value is refused (someone else took ownership - undoing
//     it is not kyacode's call).
//   - every change ends with a WM_SETTINGCHANGE broadcast, the same
//     notification setx sends, so Explorer-spawned launches pick the
//     new value up without a sign-out cycle.

const desktopEnvName = "CODEX_CLI_PATH"

var procSendMessageTimeout = syscall.NewLazyDLL("user32.dll").NewProc("SendMessageTimeoutW")

// cliPathPrior records the prior CODEX_CLI_PATH state at `on` time so
// `off` can restore it exactly.
type cliPathPrior struct {
	Absent bool   `json:"absent"`
	Value  string `json:"value"`
}

func priorRecordPath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "cli-path-prior.json"), nil
}

func readPrior() (cliPathPrior, error) {
	p, err := priorRecordPath()
	if err != nil {
		return cliPathPrior{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return cliPathPrior{Absent: true}, nil
		}
		return cliPathPrior{}, err
	}
	var prior cliPathPrior
	if err := json.Unmarshal(b, &prior); err != nil {
		// A corrupt record degrades to "prior absent" - restoring to an
		// absent value is the conservative undo.
		return cliPathPrior{Absent: true}, nil
	}
	return prior, nil
}

func writePrior(prior cliPathPrior) error {
	p, err := priorRecordPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(prior, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(body, '\n'), 0o600)
}

func removePrior() error {
	p, err := priorRecordPath()
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// broadcastEnvChange asks every top-level window to re-read the user
// environment, the same notification setx emits.
func broadcastEnvChange() {
	envPtr, err := syscall.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	var result uintptr
	const hwndBroadcast = 0xFFFF
	const wmSettingChange = 0x001A
	const smtoAbortIfHung = 0x0002
	_, _, _ = procSendMessageTimeout.Call(
		uintptr(hwndBroadcast), uintptr(wmSettingChange), 0,
		uintptr(unsafe.Pointer(envPtr)), uintptr(smtoAbortIfHung), 2000,
		uintptr(unsafe.Pointer(&result)))
}

// DesktopCLIPATHState reads the current CODEX_CLI_PATH value and reports
// whether it names our shim.
func DesktopCLIPATHState() (current string, ours bool, err error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err != nil {
		return "", false, err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(desktopEnvName)
	if err != nil {
		if err == registry.ErrNotExist {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s: %w", desktopEnvName, err)
	}
	ours, err = valueIsOurs(v)
	if err != nil {
		return v, false, err
	}
	return v, ours, nil
}

func valueIsOurs(v string) (bool, error) {
	ours, err := ExePath()
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(v), ours), nil
}

func setDesktopCLIPATH(value string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(desktopEnvName, value)
}

func deleteDesktopCLIPATH() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	err = k.DeleteValue(desktopEnvName)
	if err == registry.ErrNotExist {
		return nil
	}
	return err
}

// DesktopRegistryOn wires the desktop override: our shim path into
// CODEX_CLI_PATH, refusing any foreign value, recording the prior state
// for the exact `off` restore.
func DesktopRegistryOn() error {
	ours, err := ExePath()
	if err != nil {
		return err
	}
	cur, oursNow, err := DesktopCLIPATHState()
	if err != nil {
		return err
	}
	if oursNow {
		return nil
	}
	if strings.TrimSpace(cur) != "" {
		return fmt.Errorf("%s is already set to %q - kyacode does not overwrite another tool's override", desktopEnvName, cur)
	}
	if err := writePrior(cliPathPrior{Absent: true}); err != nil {
		return err
	}
	if err := setDesktopCLIPATH(ours); err != nil {
		removePrior()
		return err
	}
	broadcastEnvChange()
	return nil
}

// DesktopRegistryOff unwinds the wiring exactly: restore the recorded
// prior state only while the current value is still ours.
func DesktopRegistryOff() error {
	cur, oursNow, err := DesktopCLIPATHState()
	if err != nil {
		return err
	}
	if strings.TrimSpace(cur) == "" {
		_ = removePrior()
		return nil
	}
	if !oursNow {
		return fmt.Errorf("refusing: %s is %q, not kyacode's shim - remove it manually if that is intended", desktopEnvName, cur)
	}
	prior, err := readPrior()
	if err != nil {
		return err
	}
	if prior.Absent || prior.Value == "" {
		if err := deleteDesktopCLIPATH(); err != nil {
			return err
		}
	} else {
		if err := setDesktopCLIPATH(prior.Value); err != nil {
			return err
		}
	}
	broadcastEnvChange()
	_ = removePrior()
	return nil
}
