package hygiene

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The release-asset grammar's declared owner: the matrix include rows in
// .github/workflows/release.yml are the target list, and the naming shape
// (kyacode-<goos>-<goarch><ext> plus the .sha256 sidecar) is pinned where
// the producer steps carry it. Every consumer — the README fences, the
// fence fixtures' served-asset keys, the gh --pattern assertion — derives
// from here, so a producer-side rename or a dropped matrix target reds
// the consumers instead of 404ing a platform's install while the suite
// stays green.

// releaseTarget is one matrix include row.
type releaseTarget struct{ goos, goarch string }

// releaseTargets parses the build matrix out of release.yml. A row
// missing goos or goarch fails: the derived asset names would be garbage.
func releaseTargets(t *testing.T) []releaseTarget {
	t.Helper()
	var doc struct {
		Jobs map[string]struct {
			Strategy struct {
				Matrix struct {
					Include []struct {
						Goos   string `yaml:"goos"`
						Goarch string `yaml:"goarch"`
					} `yaml:"include"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(repoFile(t, ".github/workflows/release.yml"), &doc); err != nil {
		t.Fatalf("release.yml is not valid workflow YAML: %v", err)
	}
	build, ok := doc.Jobs["build"]
	if !ok {
		t.Fatal("release.yml lost its build job")
	}
	rows := build.Strategy.Matrix.Include
	if len(rows) == 0 {
		t.Fatal("release.yml lost the build matrix")
	}
	out := make([]releaseTarget, 0, len(rows))
	for _, r := range rows {
		if r.Goos == "" || r.Goarch == "" {
			t.Fatalf("matrix row %+v is missing goos/goarch — the derived asset names would be garbage", r)
		}
		out = append(out, releaseTarget{r.Goos, r.Goarch})
	}
	return out
}

// assetName derives the release-asset name for a matrix target. The ext
// rule is pinned TEXTUALLY where the producer carries it (release.yml's
// `ext=""` plus the windows→.exe assignment) — hardcoding windows→.exe
// inside this pin would recreate the hand-copy disease inside the pin
// itself; the textual pin means a changed ext rule reds here first.
func assetName(t *testing.T, tgt releaseTarget) string {
	t.Helper()
	b := string(repoFile(t, ".github/workflows/release.yml"))
	for _, want := range []string{`ext=""`, `[ "$GOOS" = "windows" ] && ext=".exe"`} {
		if !strings.Contains(b, want) {
			t.Fatalf("release.yml lost the ext rule (%q); the derived asset names cannot be trusted", want)
		}
	}
	name := "kyacode-" + tgt.goos + "-" + tgt.goarch
	if tgt.goos == "windows" {
		name += ".exe"
	}
	return name
}

// TestReleaseProducerCarriesTheGrammarShape pins the producer steps'
// naming literals against the derived shape: the out literal, the
// sidecar line, the artifact name, the upload glob, the installer-script
// attachment copies, and the asset-count equality must all carry the
// kyacode-<goos>-<goarch> grammar the matrix declares.
func TestReleaseProducerCarriesTheGrammarShape(t *testing.T) {
	b := string(repoFile(t, ".github/workflows/release.yml"))
	for _, want := range []string{
		`out="kyacode-${GOOS}-${GOARCH}${ext}"`,
		`shasum -a 256 "${out}" > "${out}.sha256"`,
		`name: kyacode-${{ matrix.goos }}-${{ matrix.goarch }}`,
		`dist/kyacode-*`,
		`cp scripts/install.sh dist/kyacode-install.sh`,
		`cp scripts/install.ps1 dist/kyacode-install.ps1`,
		`-eq "$(ls dist/kyacode-* | wc -l)"`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("release.yml lost the producer literal %q — the asset grammar's shape broke at the source", want)
		}
	}
}

// TestReadmeNamesOnlyDeclaredAssets pins the README cross-reference at
// pattern level PLUS the one concrete asset the README names (the
// manual-Windows row, derived from the windows matrix row and the ext
// rule). The README derives os/arch at runtime and never enumerates the
// full list — a full biconditional was descoped (decision record) — but
// the shape prose and the concrete row must still agree with the
// producer.
func TestReadmeNamesOnlyDeclaredAssets(t *testing.T) {
	line := readmeLine(t, "kyacode-<goos>-<goarch>")
	for _, want := range []string{"goos", "goarch", ".exe"} {
		if !strings.Contains(line, want) {
			t.Errorf("README pattern prose %q must carry the producer shape token %q", line, want)
		}
	}
	windows := ""
	for _, tgt := range releaseTargets(t) {
		if tgt.goos == "windows" {
			windows = assetName(t, tgt)
		}
	}
	if windows == "" {
		t.Fatal("the matrix lost its windows row; the README's manual-Windows row has no producer")
	}
	if !strings.Contains(string(repoFile(t, "README.md")), "`"+windows+"`") {
		t.Errorf("README must name the derived windows asset %q in the manual-Windows row", windows)
	}
}

// TestPlatformMapCoversTheReleaseMatrix pins the biconditional between
// the release matrix and the two installer platform maps that consume
// it: install.sh owns the POSIX half (os-case + arch-case membership),
// install.ps1 owns the windows half (the AMD64 mapping arm; an ARM64
// throw forbids the matrix from growing windows/arm64).
func TestPlatformMapCoversTheReleaseMatrix(t *testing.T) {
	sh := string(repoFile(t, "scripts/install.sh"))
	ps1 := string(repoFile(t, "scripts/install.ps1"))

	// region extracts a sh case block: from the line carrying start to the
	// first bare esac.
	region := func(text, marker string) string {
		lines := strings.Split(text, "\n")
		var out []string
		in := false
		for _, l := range lines {
			if !in && strings.Contains(l, marker) {
				in = true
			}
			if in {
				out = append(out, l)
				if strings.TrimSpace(l) == "esac" {
					break
				}
			}
		}
		return strings.Join(out, "\n")
	}
	osCase := region(sh, `case "$os" in`)
	archCase := region(sh, `case "$arch" in`)
	if osCase == "" || archCase == "" {
		t.Fatalf("install.sh lost its platform case blocks — the map cannot be pinned")
	}

	for _, tgt := range releaseTargets(t) {
		if tgt.goos == "windows" {
			if !strings.Contains(ps1, `'AMD64' { $asset = 'kyacode-windows-amd64.exe' }`) {
				t.Errorf("install.ps1's switch lost the windows/amd64 mapping arm")
			}
			continue
		}
		if !strings.Contains(osCase, tgt.goos) {
			t.Errorf("install.sh's os-case lost the matrix row %s", tgt.goos)
		}
		if !strings.Contains(archCase, tgt.goarch) {
			t.Errorf("install.sh's arch-case lost the matrix row %s/%s", tgt.goos, tgt.goarch)
		}
	}

	// The matrix may not grow a windows/arm64 row while install.ps1 still
	// throws on ARM64: a mapping arm is the price of that row.
	if strings.Contains(ps1, `'ARM64' { throw`) {
		for _, tgt := range releaseTargets(t) {
			if tgt.goos == "windows" && tgt.goarch == "arm64" {
				t.Errorf("the matrix grew windows/arm64 but install.ps1 still throws on ARM64 — add the mapping arm or drop the row")
			}
		}
	}
}
