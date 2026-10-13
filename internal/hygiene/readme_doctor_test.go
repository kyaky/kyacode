package hygiene

import (
	"strings"
	"testing"
)

// TestReadmeDoctorRowStatesTheVerdictTruth pins the doctor command row to
// the post-2026-09-15 semantics: five probe rows over three entry
// protocols, with the beta-header check WARNING-level (non-blocking) and
// verdicts classified per entry protocol. The old row promised
// "beta-header tolerance" as a blocking verdict the ladder no longer
// renders — a README over-claiming a gate the module retired is exactly
// the doc-fact drift this package exists to red (the 2026-09-14 operator
// report's beta row passed as tolerance it never proved).
func TestReadmeDoctorRowStatesTheVerdictTruth(t *testing.T) {
	readme := strings.Join(strings.Fields(string(repoFile(t, "README.md"))), " ")

	if !strings.Contains(readme, "five probes") {
		t.Error("doctor row lost its probe-count claim; the pre-flight is five rows (four wire probes + the model list)")
	}
	if !strings.Contains(readme, "three entry protocols") {
		t.Error("doctor row must name the three entry protocols (anthropic, openai-chat, responses) — 404 answers are classified per entry protocol")
	}
	if !strings.Contains(readme, "warning-level beta-header check") {
		t.Error("doctor row must phrase the beta check as warning-level (non-blocking), not as a tolerance verdict")
	}
	if strings.Contains(readme, "beta-header tolerance") {
		t.Error("stale claim: beta-header tolerance as a blocking check was retired 2026-09-15 — the row warns, it does not gate")
	}
}
