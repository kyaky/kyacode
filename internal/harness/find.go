package harness

import (
	"os"
	"os/exec"
	"path/filepath"
)

// findIn checks PATH first, then well-known fallback paths.
func findIn(name string, fallbacks []string) (string, bool) {
	if p, err := exec.LookPath(name); err == nil {
		return p, true
	}
	for _, f := range fallbacks {
		if _, err := os.Stat(f); err == nil {
			return f, true
		}
	}
	return "", false
}

// homePaths builds $HOME/<dir>/<name> candidates.
func homePaths(name string, dirs ...string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	suffix := name
	if runtimeGOOS() == "windows" {
		suffix = name + ".exe"
	}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, filepath.Join(home, d, suffix))
	}
	return out
}
