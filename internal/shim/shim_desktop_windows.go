//go:build windows

package shim

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The desktop personalities ride on ONE question: was the engine spawned
// by a kyacode-launched desktop instance (its ancestor carries our
// electron-profile marker on the command line) or by the user's everyday
// desktop instance? The CODEX_CLI_PATH override is registry-delivered and
// therefore reaches BOTH instances - the discriminator has to live at
// spawn time, in the shim, or the user's signed-in everyday app would be
// wired onto the key-free gateway home and stall on its account load
// (observed 2026-11-02 before this split existed).
//
// The walk deliberately avoids the tool-help snapshot: the desktop host
// spawns its engine under a token where the snapshot was observed to
// fail, while OpenProcess+NtQueryInformationProcess on the ancestor
// chain succeeds. Each hop takes the parent pid straight from the
// PROCESS_BASIC_INFORMATION's InheritedFromUniqueProcessId field, the
// image name from QueryFullProcessImageNameW, and the command line from
// the ancestor's PEB.
const (
	desktopModeNone     = iota // no desktop ancestry: the extension path decides
	desktopModeVanilla         // desktop ancestry without the marker: vanilla passthrough
	desktopModeGateway         // kyacode-launched desktop instance: gateway wiring
)

// desktopHostExe is the desktop app's process image name (lowercased).
const desktopHostExe = "chatgpt.exe"

// markerArg is the command-line fragment that marks a kyacode-launched
// desktop instance: the launcher always passes the dedicated electron
// profile directory.
func markerArg() string {
	return "--user-data-dir=" + ElectronProfileDirValue()
}

var (
	modNtdll            = syscall.NewLazyDLL("ntdll.dll")
	procNtQueryProcess  = modNtdll.NewProc("NtQueryInformationProcess")
	procReadMemory      = syscall.NewLazyDLL("kernel32.dll").NewProc("ReadProcessMemory")
	procOpenProcessRead = syscall.NewLazyDLL("kernel32.dll").NewProc("OpenProcess")
	procQueryImageName  = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW")
)

const (
	processQueryLimited = 0x1000
	processVmRead       = 0x0010
	processBasicInfo    = 0 // PROCESS_BASIC_INFORMATION class
)

// openForRead opens pid for PEB reads, failing with the errno captured
// for the diagnostics.
func openForRead(pid int) (uintptr, string) {
	h, _, callErr := procOpenProcessRead.Call(uintptr(processQueryLimited|processVmRead), 0, uintptr(pid))
	if h == 0 {
		return 0, "errno=" + errnoText(callErr)
	}
	return h, ""
}

// errnoText renders a LazyProc error's errno number (the message text is
// locale-dependent; the number is the stable diagnostic).
func errnoText(callErr error) string {
	if ne, ok := callErr.(syscall.Errno); ok {
		return fmt.Sprintf("%d", uint32(ne))
	}
	return "0"
}

// basicInfo runs NtQueryInformationProcess(ProcessBasicInformation) and
// returns the parent pid (InheritedFromUniqueProcessId) plus the PEB
// address. The x64 PROCESS_BASIC_INFORMATION is six pointers; the sixth
// carries the parent.
func basicInfo(h uintptr) (parent int, peb uintptr, ok bool) {
	var pbi [6]uintptr
	r1, _, _ := procNtQueryProcess.Call(h, uintptr(processBasicInfo),
		uintptr(unsafe.Pointer(&pbi[0])), 48, 0)
	if r1 != 0 {
		return 0, 0, false
	}
	return int(pbi[5]), pbi[1], true
}

// processImageName reads an ancestor's full image path (kernel32
// QueryFullProcessImageNameW) and returns the lowercased BASE name - the
// packaged app's full path lives under WindowsApps and must never be
// compared against the bare exe name directly.
func processImageName(h uintptr) string {
	var buf [windows.MAX_PATH]uint16
	var size uint32 = uint32(len(buf))
	r1, _, _ := procQueryImageName.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)), 0)
	if r1 == 0 {
		return ""
	}
	return strings.ToLower(filepath.Base(windows.UTF16ToString(buf[:size])))
}

// ancestorCmdline reads an ancestor's command line through its PEB
// (ProcessParameters->CommandLine, the UNICODE_STRING at offset 0x70
// whose Buffer/Length convention the Environment read at 0x80 already
// proved on this exact spawn shape). A read that fails for any reason
// reports not-ok: the caller's fail-toward-vanilla rule handles it.
func ancestorCmdline(pid int) (string, bool) {
	h, _ := openForRead(pid)
	if h == 0 {
		return "", false
	}
	defer syscall.CloseHandle(syscall.Handle(h))
	_, peb, ok := basicInfo(h)
	if !ok || peb == 0 {
		return "", false
	}
	var paramsPtr uintptr
	if !readUint64(h, peb+0x20, &paramsPtr) || paramsPtr == 0 {
		return "", false
	}
	var us [16]byte
	if !readBytes(h, paramsPtr+0x70, us[:]) {
		return "", false
	}
	length := int(uint16(us[0]) | uint16(us[1])<<8)
	bufPtr := uintptr(us[8]) | uintptr(us[9])<<8 | uintptr(us[10])<<16 | uintptr(us[11])<<24 |
		uintptr(us[12])<<32 | uintptr(us[13])<<40 | uintptr(us[14])<<48 | uintptr(us[15])<<56
	if length <= 0 || length > 32768 || bufPtr == 0 {
		return "", false
	}
	buf := make([]byte, length)
	if !readBytes(h, bufPtr, buf) {
		return "", false
	}
	return windows.UTF16ToString(bufToUint16(buf)), true
}

func readUint64(h uintptr, addr uintptr, out *uintptr) bool {
	var b [8]byte
	if !readBytes(h, addr, b[:]) {
		return false
	}
	var v uintptr
	for i := 7; i >= 0; i-- {
		v = v<<8 | uintptr(b[i])
	}
	*out = v
	return true
}

func readBytes(h uintptr, addr uintptr, buf []byte) bool {
	var got uintptr
	r1, _, _ := procReadMemory.Call(h, addr, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&got)))
	return r1 != 0
}

func bufToUint16(b []byte) []uint16 {
	out := make([]uint16, len(b)/2)
	for i := range out {
		out[i] = uint16(b[i*2]) | uint16(b[i*2+1])<<8
	}
	return out
}

// cmdlineMatchesMarker reports whether an ancestor command line carries
// the marker: Electron quotes the --user-data-dir value in its own argv,
// so the comparison strips quote characters from the line before the
// contains check. Pure so the matching rule is testable without any
// process.
func cmdlineMatchesMarker(line, marker string) bool {
	flat := strings.ReplaceAll(strings.ToLower(line), `"`, "")
	return strings.Contains(flat, marker)
}

// resolveDesktopMode walks the ancestor chain entirely through
// OpenProcess+NtQuery (no tool-help snapshot): parent pids from the
// basic-information record, image names from QueryFullProcessImageName,
// command lines from the PEB. Uncertainty fails toward the everyday
// app's safety: an ancestor that cannot be read is skipped, and only a
// POSITIVE marker match selects the gateway wiring - an unreadable
// desktop ancestry can never wire the gateway home, it can only run
// vanilla (the recorded runtime, no env injection, the shape the
// gateway session already proved the desktop UI tolerates).
func resolveDesktopModeDiag() (int, []string) {
	lines := []string{}
	marker := strings.ToLower(markerArg())
	sawDesktopHost := false
	pid := int(windows.GetCurrentProcessId())
	for hop := 0; hop < 16; hop++ {
		h, diag := openForRead(pid)
		if h == 0 {
			lines = append(lines, fmt.Sprintf("hop %d pid %d: open failed (%s)", hop, pid, diag))
			break
		}
		var parent int
		var ok bool
		parent, _, ok = basicInfo(h)
		image := processImageName(h)
		syscall.CloseHandle(syscall.Handle(h))
		if !ok {
			lines = append(lines, fmt.Sprintf("hop %d pid %d: NtQuery failed", hop, pid))
			break
		}
		lines = append(lines, fmt.Sprintf("hop %d pid %d exe=%q", hop, pid, image))
		if image == desktopHostExe {
			sawDesktopHost = true
		}
		if pid != int(windows.GetCurrentProcessId()) {
			line, ok := ancestorCmdline(pid)
			if ok {
				// The match runs against the FULL command line with quote
				// characters stripped: Electron quotes the --user-data-dir
				// value in its own argv, and a literal-quotes Contains
				// would miss it. Only the debug display truncates (the
				// marker may sit deep in a long electron argv).
				display := line
				if len(display) > 300 {
					display = display[:300]
				}
				lines = append(lines, fmt.Sprintf("  cmdline: %s", display))
				if cmdlineMatchesMarker(line, marker) {
					lines = append(lines, "  marker: MATCH")
					return desktopModeGateway, lines
				}
			} else {
				lines = append(lines, "  cmdline: <unreadable>")
			}
		}
		if parent == 0 {
			break
		}
		pid = parent
	}
	if sawDesktopHost {
		lines = append(lines, "decision: vanilla (desktop host ancestry, no marker)")
		return desktopModeVanilla, lines
	}
	lines = append(lines, "decision: none (no desktop ancestry)")
	return desktopModeNone, lines
}

func resolveDesktopMode() int {
	m, _ := resolveDesktopModeDiag()
	return m
}

// desktopVanillaRuntime resolves the desktop's OWN managed backend
// runtime: the single codex.exe under %LOCALAPPDATA%\OpenAI\Codex\bin.
// The rule is the review's: the host-selected runtime must be resolved,
// never guessed - exactly one candidate is accepted and any ambiguity
// (multiple candidate dirs, or none) fails loudly rather than picking a
// generation the running host may not be paired with. The sibling
// helper binaries travel with the runtime in the same directory, so the
// host's helper resolution (relative to the override path) keeps seeing
// its own companions.
func desktopVanillaRuntime() (string, bool, error) {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return "", false, fmt.Errorf("LOCALAPPDATA is not set")
	}
	bin := local + string(os.PathSeparator) + `OpenAI\Codex\bin`
	entries, err := os.ReadDir(bin)
	if err != nil {
		return "", false, fmt.Errorf("scan %s: %w", bin, err)
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := bin + string(os.PathSeparator) + e.Name() + string(os.PathSeparator) + "codex.exe"
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			found = append(found, candidate)
		}
	}
	switch len(found) {
	case 1:
		return found[0], true, nil
	case 0:
		return "", false, fmt.Errorf("no codex.exe under %s - the desktop's managed runtime is missing", bin)
	default:
		// Post-upgrade ambiguity: the app's updater has so far kept a
		// single codex component dir, but a upgrade that leaves the old
		// generation behind is exactly when this branch fires. The
		// newest binary is the generation the running host resolved
		// (the upgrade copies it last); the guess is loud, never silent,
		// and a wrong pick surfaces as the host's own backend error -
		// recoverable, and still better than refusing every everyday
		// spawn until someone deletes a directory by hand.
		newest := ""
		var newestAt int64
		for _, c := range found {
			fi, err := os.Stat(c)
			if err != nil {
				continue
			}
			if fi.ModTime().UnixNano() >= newestAt {
				newestAt = fi.ModTime().UnixNano()
				newest = c
			}
		}
		if newest == "" {
			return "", false, fmt.Errorf("%d codex.exe candidates under %s and none statable", len(found), bin)
		}
		fmt.Fprintf(os.Stderr, "kyacode shim: WARNING %d desktop runtime candidates under %s; using the newest (%s) - verify the desktop behaves\n", len(found), bin, newest)
		return newest, true, nil
	}
}
