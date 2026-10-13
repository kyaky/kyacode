package hygiene

import (
	"strings"
	"testing"
)

// TestReadmeStorageModeClaimsArePlatformScoped pins the storage prose to
// the verified contract: Go's os.Chmod maps numeric modes onto the
// read-only attribute on Windows (NTFS ACLs govern actual access), so
// any bare "0600" promise in the README is a unix guarantee and must say
// so. And internal/config.Save chmods 0600 after every save (not just
// the first), so the stale "only guaranteed for the first save" caveat —
// which described an older WriteFile-only implementation — must be gone.
func TestReadmeStorageModeClaimsArePlatformScoped(t *testing.T) {
	// Prose wraps hard in the markdown source; a reader sees the joined
	// sentence, so match the joined shape.
	readme := strings.Join(strings.Fields(string(repoFile(t, "README.md"))), " ")

	if strings.Contains(readme, "guaranteed for the first save") {
		t.Error("stale claim still present: config.Save re-chmods 0600 on every save (internal/config/config.go), not only the first")
	}
	if !strings.Contains(readme, "0600 on unix") {
		t.Errorf("no unix scoping on the 0600 promises; add \"0600 on unix\" plus the Windows chmod-mapping caveat")
	}
	if strings.Contains(readme, "key (stored 0600)") {
		t.Errorf("the Quick start comment promises a bare 0600 without unix scoping")
	}
}
