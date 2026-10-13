package harness

import (
	"sync"
	"testing"
)

// TestConcurrentSandboxLaunchesShareOneHomeWithoutProbeRaces stresses the
// shared-sentinel race in the sandbox package's writability probe
// (ensurePrivateDir on the ~/.kyacode root): every BuildEnv probes the
// same ~/.kyacode, so a create/remove interleave used to fail a
// concurrent launch with "not writable" (open arm) or a bare ENOENT
// (remove arm). Asserts ZERO errors of any kind on cleanup or BuildEnv —
// substring filtering would hide whichever arm regressed. Probabilistic
// red pre-fix; deterministic green after the ENOENT-tolerant probe
// removal.
func TestConcurrentSandboxLaunchesShareOneHomeWithoutProbeRaces(t *testing.T) {
	_ = withHome(t)
	// One stub for the whole test, BEFORE the goroutines: the harvest seam
	// is a package-level var, and per-call stubbing from concurrent
	// goroutines would race the swap. With it pinned, the carrier bodies
	// run their file writes without touching the installed codex.
	stubCodexHarvest(t)
	ep := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}

	const goroutines = 12
	const rounds = 20
	errs := make(chan error, goroutines*rounds*2)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				w, err := codexSpec.CarrierBuild(ep, "m1", Resolve(ep, "m1"))
				if err != nil {
					errs <- err
					continue
				}
				w.Cleanup() // err==nil implies a non-nil wire.Cleanup, as BuildEnv only fails when it returns the zero Wire
				_ = w.Env
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent BuildEnv/cleanup failed: %v", err)
	}
}
