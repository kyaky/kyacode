package hygiene

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// repoFile reads a path relative to the module root (tests here run in the
// package dir; the workflows and README live two levels up).
func repoFile(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return b
}

// workflowDoc is the minimal parsed shape of an Actions workflow file.
type workflowDoc struct {
	Jobs map[string]struct {
		Env    map[string]string `yaml:"env"`
		RunsOn string            `yaml:"runs-on"`
		Steps  []struct {
			Name string `yaml:"name"`
			Uses string `yaml:"uses"`
			Run  string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func parseWorkflow(t *testing.T, rel string) workflowDoc {
	t.Helper()
	var doc workflowDoc
	if err := yaml.Unmarshal(repoFile(t, rel), &doc); err != nil {
		t.Fatalf("%s is not valid workflow YAML: %v", rel, err)
	}
	return doc
}

// TestWorkflowActionsArePinnedToCommitShas pins every uses: in every
// workflow to a full 40-hex commit SHA from the KNOWN moveable-tag map, so
// a mutable tag ref (dependencies are fetched at run time) can never
// smuggle in new code silently. A dependabot config keeps the pins moving
// deliberately instead (the blank-line spacing below is in service of
// bump mergeability — keep it).
func TestWorkflowActionsArePinnedToCommitShas(t *testing.T) {
	// Verified tag→SHA mappings (git ls-remote, pinned at authoring time).
	// BLANK LINES between entries are load-bearing, not style: the
	// workflows' uses: lines sit just three lines apart, so back-to-back
	// map entries made every sequential dependabot bump zipconflict on
	// the adjacent row (the #15 post-mortem). The ≥2-unchanged-lines gap
	// is what git's 3-way hunk logic needs to treat two bumps touching
	// different rows as non-overlapping hunks — keep the spacing.
	known := map[string]string{
		"actions/checkout": "3d3c42e5aac5ba805825da76410c181273ba90b1", // v7.0.1

		"actions/setup-go": "b7ad1dad31e06c5925ef5d2fc7ad053ef454303e", // v7.0.0

		"actions/cache": "55cc8345863c7cc4c66a329aec7e433d2d1c52a9", // v6.1.0

		"actions/upload-artifact": "043fb46d1a93c77aae656e7c1c64a875d1fc6a0a", // v7.0.1

		"actions/download-artifact": "3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c", // v8.0.1
	}
	shaRe := regexp.MustCompile(`^[0-9a-f]{40}$`)

	for _, wf := range []string{
		".github/workflows/test.yml",
		".github/workflows/release.yml",
		".github/workflows/tagrel.yml",
	} {
		doc := parseWorkflow(t, wf)
		for jobName, job := range doc.Jobs {
			for i, step := range job.Steps {
				if step.Uses == "" {
					continue
				}
				ref := step.Uses
				t.Run(fmt.Sprintf("%s:%s/step%d:%s", filepath.Base(wf), jobName, i, ref), func(t *testing.T) {
					action, _, _ := splitRef(ref)
					want, ok := known[action]
					if !ok {
						t.Fatalf("action %q is not in the known-pin table; add its verified commit SHA before using it", action)
					}
					if !shaRe.MatchString(want) {
						t.Fatalf("pin table entry for %q is not a full 40-hex sha", action)
					}
					_, pinned, _ := splitRef(ref)
					if pinned != want {
						t.Fatalf("%s must be pinned to the verified commit %s (got ref %q)", action, want, ref)
					}
				})
			}
		}
	}
}

func splitRef(uses string) (action, ref string, ok bool) {
	for i := 0; i < len(uses); i++ {
		if uses[i] == '@' {
			return uses[:i], uses[i+1:], true
		}
	}
	return uses, "", false
}

// TestWorkflowPinsAreKeptMoving asserts the supply-chain complement of
// the SHA pinning: a dependabot config covering BOTH moving surfaces —
// github-actions (the pinned commit SHAs re-pinned deliberately when a
// release of an action is wanted) and gomod (the module's own
// dependencies, which CI's govulncheck job reports on).
func TestWorkflowPinsAreKeptMoving(t *testing.T) {
	var doc struct {
		Version int `yaml:"version"`
		Updates []struct {
			PackageEcosystem string `yaml:"package-ecosystem"`
			Directory        string `yaml:"directory"`
		} `yaml:"updates"`
	}
	if err := yaml.Unmarshal(repoFile(t, ".github/dependabot.yml"), &doc); err != nil {
		t.Fatalf(".github/dependabot.yml is not valid YAML: %v", err)
	}
	if doc.Version != 2 {
		t.Errorf("dependabot version = %d, want 2", doc.Version)
	}
	foundActions, foundGomod := false, false
	for _, u := range doc.Updates {
		switch {
		case u.PackageEcosystem == "github-actions" && u.Directory == "/":
			foundActions = true
		case u.PackageEcosystem == "gomod" && u.Directory == "/":
			foundGomod = true
		}
	}
	if !foundActions {
		t.Error("dependabot.yml must declare a github-actions update over / so the SHA pins keep moving")
	}
	if !foundGomod {
		t.Error("dependabot.yml must declare a gomod update over / so the module's dependencies keep moving")
	}
}

// TestTestWorkflowRunsGovulncheckPinnedToAModuleVersion pins the
// vulnerability scan job (#14 companion): reachable vulnerabilities fail
// the build on PR and push. The step is run-based (`go run
// golang.org/x/vuln/cmd/govulncheck@vX ./...`) precisely so NO new
// action enters the SHA pin table — the scan is pinned to a module
// version, not a moveable tag, and the watchdogs stay simple.
func TestTestWorkflowRunsGovulncheckPinnedToAModuleVersion(t *testing.T) {
	doc := parseWorkflow(t, ".github/workflows/test.yml")
	job, ok := doc.Jobs["vulnscan"]
	if !ok {
		t.Fatalf("test.yml must carry a `vulnscan` job (nothing scans the module's dependencies for reachable CVEs otherwise)")
	}
	var scan bool
	for _, s := range job.Steps {
		if s.Run == "" {
			continue
		}
		if strings.Contains(s.Run, "golang.org/x/vuln/cmd/govulncheck@v") && strings.Contains(s.Run, "./...") {
			scan = true
		}
	}
	if !scan {
		t.Errorf("vulnscan job must run govulncheck FOR THE WHOLE MODULE via a go run pinned to a module version (got steps %+v)", job.Steps)
	}
}

// TestTestWorkflowRunsLintTierPinnedToAModuleVersion pins the first
// lint tier (#15 companion): staticcheck beyond vet, zero findings is
// the bar, scope frozen by the checked-in staticcheck.conf. Run-based
// (`go run honnef.co/go/tools/cmd/staticcheck@vX ./...`) like the scan
// — no new action in the SHA pin table.
func TestTestWorkflowRunsLintTierPinnedToAModuleVersion(t *testing.T) {
	doc := parseWorkflow(t, ".github/workflows/test.yml")
	job, ok := doc.Jobs["lint"]
	if !ok {
		t.Fatalf("test.yml must carry a `lint` job (there is no quality tier beyond vet otherwise)")
	}
	var lint bool
	for _, s := range job.Steps {
		if strings.Contains(s.Run, "cmd/staticcheck@v") && strings.Contains(s.Run, "./...") {
			lint = true
		}
	}
	if !lint {
		t.Errorf("lint job must run staticcheck FOR THE WHOLE MODULE via a go run pinned to a module version (got steps %+v)", job.Steps)
	}
}

// TestTestWorkflowRunsVetAndTestOnWindowsRunner pins the CI presence of
// the platform-coupled behaviors (windows chmod mapping, RemoveAll retry,
// .cmd shim discovery — none executable on the ubuntu leg): the `test` job
// must run on BOTH runners with the same vet/test commands and CGO off —
// unchanged in shape since the pin was authored. Jobs are pinned by NAME
// in a named-jobs set: adding a job to test.yml means landing it here
// deliberately with its own shape pin (the race job has one below).
func TestTestWorkflowRunsVetAndTestOnWindowsRunner(t *testing.T) {
	var doc struct {
		Env  map[string]string `yaml:"env"`
		Jobs map[string]struct {
			RunsOn   string            `yaml:"runs-on"`
			Env      map[string]string `yaml:"env"`
			Strategy struct {
				Matrix struct {
					Os []string `yaml:"os"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(repoFile(t, ".github/workflows/test.yml"), &doc); err != nil {
		t.Fatalf("test.yml is not valid workflow YAML: %v", err)
	}
	// Named-jobs pin: every job in the workflow must carry a deliberate
	// shape pin below, so a job added without one fails here first, before
	// CI fails the run.
	for name := range doc.Jobs {
		switch name {
		case "test", "race", "vulnscan", "lint":
		default:
			t.Errorf("unpinned job %q — extend this pin deliberately when adding a CI job to test.yml", name)
		}
	}
	job, ok := doc.Jobs["test"]
	if !ok {
		t.Fatalf("test.yml lost its `test` job (the pinned vet+test gate)")
	}
	runners := job.Strategy.Matrix.Os
	if len(runners) == 0 && job.RunsOn != "" {
		runners = []string{job.RunsOn}
	}
	t.Run("covers both runner families", func(t *testing.T) {
		has := map[string]bool{}
		for _, r := range runners {
			if r == "${{ matrix.os }}" {
				t.Fatalf("runs-on stayed symbolic; the matrix must list runners (got %v)", runners)
			}
			has[r] = true
		}
		if !has["ubuntu-latest"] || !has["windows-latest"] {
			t.Errorf("runner matrix = %v, want ubuntu-latest AND windows-latest", runners)
		}
	})
	t.Run("still vets and tests", func(t *testing.T) {
		var vet, test bool
		for _, s := range job.Steps {
			vet = vet || s.Run == "go vet ./..."
			test = test || s.Run == "go test ./..."
		}
		if !vet || !test {
			t.Errorf("vet step found=%v test step found=%v (the legs must run the identical commands)", vet, test)
		}
		envCGO, inJobEnv := job.Env["CGO_ENABLED"]
		if !inJobEnv {
			envCGO = doc.Env["CGO_ENABLED"]
		}
		if envCGO != "0" {
			t.Errorf("CGO_ENABLED = %q, want \"0\" (CI must match the release recipe)", envCGO)
		}
	})
}

// TestTestWorkflowArmsAJobTheRaceDetectorCanRun pins the race arm (#04
// companion): -race requires cgo, which the CGO-off pinned job forbids by
// design (it must match the release recipe), so the detector rides its
// own job — CGO on, `go test -race ./...`, one linux leg. The pinned
// vet+test job above keeps its exact shape; this pin covers the new job
// so wording changes fail hygiene before CI wastes a run.
func TestTestWorkflowArmsAJobTheRaceDetectorCanRun(t *testing.T) {
	doc := parseWorkflow(t, ".github/workflows/test.yml")
	job, ok := doc.Jobs["race"]
	if !ok {
		t.Fatalf("test.yml must carry a `race` job — the detector cannot run under the CGO-off pinned job")
	}
	if job.RunsOn != "ubuntu-latest" {
		t.Errorf("race job runs-on = %q, want ubuntu-latest (one linux leg is the deliberate scope; windows -race is out of scope)", job.RunsOn)
	}
	envCGO, inJobEnv := job.Env["CGO_ENABLED"]
	if !inJobEnv || envCGO != "1" {
		t.Errorf("race job CGO_ENABLED = %q (job env %v), want \"1\" — -race cannot run without cgo", envCGO, job.Env)
	}
	var raceStep bool
	for _, s := range job.Steps {
		if s.Run == "go test -race ./..." {
			raceStep = true
		}
	}
	if !raceStep {
		t.Errorf("race job must run exactly `go test -race ./...` (got steps %+v)", job.Steps)
	}
}

// TestTestWorkflowDelegatesCachingToSetupGo pins the single-cache-layer
// shape: setup-go@v5's built-in go.sum-keyed cache replaces the hand
// actions/cache step (same never-changing 4-line go.sum, and the built-in
// cache keys per runner automatically for the windows leg). release.yml's
// multi-target matrix is deliberately NOT folded into this pin.
func TestTestWorkflowDelegatesCachingToSetupGo(t *testing.T) {
	doc := parseWorkflow(t, ".github/workflows/test.yml")
	foundSetupGo, foundCache := false, false
	for _, job := range doc.Jobs {
		for _, s := range job.Steps {
			action, _, _ := splitRef(s.Uses)
			switch action {
			case "actions/setup-go":
				foundSetupGo = true
			case "actions/cache":
				foundCache = true
			}
		}
	}
	if !foundSetupGo {
		t.Error("setup-go step vanished; the build has no Go toolchain")
	}
	if foundCache {
		t.Error("test.yml must not layer actions/cache on top of setup-go's built-in go.sum-keyed cache")
	}
}
