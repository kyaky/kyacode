package main

// The in-process CLI battery (round-2 ticket 04): command bodies report
// through writer parameters, so the report rows, the doctor red-path
// verdict, and the auth refusal rows are checkable WITHOUT rebuilding
// the binary and spawning a subprocess. Spawn-level behavior stays
// pinned by main_test.go unchanged — the default channels ARE the old
// global streams; the topology (which port a line rides) does not move.

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kyacode/internal/config"
)

func TestWriterDefaultsAreTheRealStreams(t *testing.T) {
	dc := defaultChannels()
	if dc.stdout != os.Stdout || dc.stderr != os.Stderr {
		t.Fatalf("writer defaults = (%T, %T), want the global streams the spawn-level pins stand on", dc.stdout, dc.stderr)
	}
}

// The status overview's report rows in-process: gateway row, friendly
// not-stored key row, the swept-sandbox accounting and specificity, and
// the harness table — one cmdStatus call, no subprocess, no binary
// rebuild, with the warning channel empty (a healthy sweep warns about
// nothing).
func TestStatusReportRowsInProcess(t *testing.T) {
	withHermeticHome(t)
	home := os.Getenv("HOME")

	// One expired codex-shaped sandbox: the sweep's report rows have a
	// real row to speak about.
	stuck := filepath.Join(home, ".kyacode", "codex-123-456")
	if err := os.MkdirAll(stuck, 0o700); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(stuck, stamp, stamp); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	var out, errs bytes.Buffer
	err := cmdStatus(channels{stdout: &out, stderr: &errs})
	if err != nil {
		t.Fatalf("cmdStatus: %v", err)
	}
	if errs.Len() != 0 {
		t.Errorf("a healthy sweep must leave the warning channel empty: %q", errs.String())
	}
	for _, row := range []string{
		"gateway: " + config.DefaultGateway,
		"key:     not stored (run `kyacode auth`)",
		"swept 1 orphaned sandbox(es)",
		"  removed " + stuck,
		"HARNESS",
	} {
		if !strings.Contains(out.String(), row) {
			t.Errorf("stdout missing the report row %q; got: %q", row, out.String())
		}
	}
}

// The doctor red path in-process: banner row, then the failing probe
// rows carrying the gateway module's diagnosis, then the returned
// verdict — the whole decline conversation captured without spawning
// the binary. The gateway is a just-closed loopback server: connection
// refused, hermetic by construction.
func TestDoctorRedPathVerdictInProcess(t *testing.T) {
	withHermeticHome(t)
	dead := httptest.NewServer(nil)
	dead.Close() // live only long enough to claim the port; now everything refuses
	t.Setenv("KYA_GATEWAY", dead.URL)
	// The banner and Load both run ValidateGateway: plaintext http needs
	// the loopback opt-in for the gateway to be servable at all (the
	// elements the red path rides, not the red path's subject).
	t.Setenv("KYA_GATEWAY_INSECURE", "1")

	home := os.Getenv("HOME")
	if err := os.MkdirAll(filepath.Join(home, ".kyacode"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".kyacode", "token"), []byte("sk-test-123456\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errs bytes.Buffer
	err := cmdDoctor(channels{stdout: &out, stderr: &errs})
	if err == nil || err.Error() != "gateway pre-flight FAILED — direct connect cannot proceed" {
		t.Fatalf("doctor red path = %v, want the exact verdict sentence", err)
	}
	if !strings.Contains(out.String(), "gateway: "+dead.URL) {
		t.Errorf("missing the banner row: %q", out.String())
	}
	if !strings.Contains(out.String(), "anthropic /v1/messages") {
		t.Errorf("missing the failing probe rows: %q", out.String())
	}
	if errs.Len() != 0 {
		t.Errorf("doctor reports on stdout only; stderr got: %q", errs.String())
	}
}

// The auth refusal rows in-process — the argv gate declines before any
// credential read, so driving run() with buffers cannot accidentally
// read the test process's stdin and no token file appears.
func TestAuthRefusalRowsInProcess(t *testing.T) {
	withHermeticHome(t)
	home := os.Getenv("HOME")
	// No root, no token: a refused call must leave both absent.
	tokenPath := filepath.Join(home, ".kyacode", "token")

	for _, args := range [][]string{
		{"auth", "sk-inline-pasted-key"},
		{"auth", "--stdin", "extra"},
	} {
		var out, errs bytes.Buffer
		err := run(args, nil, channels{stdout: &out, stderr: &errs})
		if err == nil || !strings.Contains(err.Error(), "auth takes no arguments beyond `--stdin`") {
			t.Errorf("%v: refused run = %v, want the counting refusal", args, err)
		}
		if _, statErr := os.Stat(tokenPath); !os.IsNotExist(statErr) {
			t.Errorf("%v: a refused call must not have stored a token: %v", args, statErr)
		}
		if out.Len() != 0 || errs.Len() != 0 {
			t.Errorf("%v: the refusal lives in the returned error, not the channels: stdout %q stderr %q", args, out.String(), errs.String())
		}
	}
}
