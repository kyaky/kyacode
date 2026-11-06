// Package claude3p owns the Claude Desktop third-party-inference (3P)
// configuration: the two files kyacode writes under
// %LOCALAPPDATA%\Claude-3p\configLibrary so the desktop app's Code tab
// runs on the KYA gateway.
//
// The loader contract is pinned against the installed app binary
// (Claude Desktop 4.4212.0.0, app.asar): the loader does NOT scan the
// directory - it reads _meta.json ({appliedId, entries}), validates
// appliedId against a 36-char UUID-shaped regex, then reads exactly one
// file named <appliedId>.json. The app manifest excludes this directory
// from MSIX filesystem write virtualization (Anthropic's own build
// comment: 3P userData lives in LocalAppData), so files kyacode writes
// to the real path are seen by the packaged app.
//
// The credential never rests in these files: the config points the app
// at kyacode itself as the credential helper
// (inferenceCredentialHelperWindows + helper args), and the helper
// prints the token from ~/.kyacode/token at call time - unauth and key
// rotation take effect on the helper's next invocation, and the key is
// never DPAPI-sealed into the app's config the way the in-app form
// seals inferenceGatewayApiKey values.
//
// Ownership doctrine (the same shape as the codex desktop registry
// wiring): `on` refuses to touch a configLibrary that already holds a
// configuration kyacode did not write; `off` removes exactly our files
// and never anything the app or the user wrote.
package claude3p

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Dir returns the Claude-3p root for the platform. Windows is the only
// implemented target: the desktop route was verified against the MSIX
// app there, and the macOS/Linux roots are documented but untested.
func Dir() (string, error) {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return "", fmt.Errorf("LOCALAPPDATA is not set")
	}
	return filepath.Join(local, "Claude-3p"), nil
}

// ConfigLibraryDir is the directory the app's loader reads.
func ConfigLibraryDir() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "configLibrary"), nil
}

// entryName is the display name our configuration carries in _meta.json.
const entryName = "kyacode gateway"

// uuidv4 formats a random RFC 4122 version-4 UUID (the 36-char shape the
// app's loader regex demands).
func uuidv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	h := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	return h, nil
}

// meta is the _meta.json shape the loader reads: appliedId selects the
// active entry file; every entry id must be listed here.
type meta struct {
	AppliedID string      `json:"appliedId"`
	Entries   []metaEntry `json:"entries"`
}

type metaEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	Note     string `json:"note,omitempty"`
}

// modelEntry is one inferenceModels entry (the modern object form). name
// is the model id the gateway serves; behavesAs gives the session the
// Claude-model behavior (effort control, context assumptions) that
// non-Claude ids otherwise lose; maxEffort lifts the default 'low' cap
// that would otherwise gut the reasoning tiers.
type modelEntry struct {
	Name      string `json:"name"`
	BehavesAs string `json:"behavesAs,omitempty"`
	MaxEffort string `json:"maxEffort,omitempty"`
}

// config is the flat v1 configuration body: the managed-configuration
// key names at the top level, no wrapper. The credential rides the
// helper, never a key field.
type config struct {
	InferenceProvider                string       `json:"inferenceProvider"`
	InferenceGatewayBaseURL          string       `json:"inferenceGatewayBaseUrl"`
	InferenceGatewayAuthScheme       string       `json:"inferenceGatewayAuthScheme"`
	InferenceCredentialKind          string       `json:"inferenceCredentialKind"`
	InferenceCredentialHelperWindows string       `json:"inferenceCredentialHelperWindows"`
	InferenceCredentialHelperArgs    []string     `json:"inferenceCredentialHelperArgs"`
	InferenceModels                  []modelEntry `json:"inferenceModels"`
	ChatTabEnabled                   bool         `json:"chatTabEnabled"`
}

// HelperArgs is the argument vector the config points the app at: the
// kyacode binary invoked with this subcommand prints the gateway token
// on stdout (the app's documented helper contract: bare token, exit 0).
var HelperArgs = []string{"claude-desktop-cred"}

// Build renders the two files kyacode writes: the _meta.json body and
// the <id>.json configuration body. models are the gateway's chat model
// ids in display order (the first entry becomes the picker's default);
// baseURL is the gateway base WITHOUT /v1 (the app maps it to
// ANTHROPIC_BASE_URL and appends /v1/messages itself); helperPath is the
// absolute path of the kyacode binary acting as the credential helper.
func Build(models []string, baseURL, helperPath string) (metaBody []byte, id string, cfgBody []byte, err error) {
	if baseURL == "" {
		return nil, "", nil, fmt.Errorf("empty gateway base URL")
	}
	if helperPath == "" {
		return nil, "", nil, fmt.Errorf("empty credential helper path")
	}
	if len(models) == 0 {
		return nil, "", nil, fmt.Errorf("no gateway models to publish (check the gateway with `kyacode doctor`)")
	}
	id, err = uuidv4()
	if err != nil {
		return nil, "", nil, err
	}
	entries := make([]modelEntry, 0, len(models))
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		entries = append(entries, modelEntry{Name: m, BehavesAs: "claude-sonnet-5", MaxEffort: "high"})
	}
	if len(entries) == 0 {
		return nil, "", nil, fmt.Errorf("no usable gateway models to publish")
	}
	cfg := config{
		InferenceProvider:                "gateway",
		InferenceGatewayBaseURL:          baseURL,
		InferenceGatewayAuthScheme:       "bearer",
		InferenceCredentialKind:          "helper-script",
		InferenceCredentialHelperWindows: helperPath,
		InferenceCredentialHelperArgs:    HelperArgs,
		InferenceModels:                  entries,
		// The Chat tab hides in 3P mode unless the profile enables it
		// (field-tested by the omlx writer); with it, the user's chat
		// usage continues through the gateway models.
		ChatTabEnabled: true,
	}
	cfgBody, err = json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, "", nil, err
	}
	m := meta{
		AppliedID: id,
		Entries:   []metaEntry{{ID: id, Name: entryName, Provider: "gateway"}},
	}
	metaBody, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, "", nil, err
	}
	return metaBody, id, cfgBody, nil
}

// State classifies the configLibrary directory's ownership.
type State int

const (
	// StateAbsent: no _meta.json - the app runs first-party (or the user
	// removed everything).
	StateAbsent State = iota
	// StateOurs: our applied entry is the active one.
	StateOurs
	// StateOursUnapplied: our entry file exists but the applied id moved
	// (the in-app window wrote its own configuration after us).
	StateOursUnapplied
	// StateForeign: a configuration kyacode did not write is applied.
	StateForeign
)

// ReadState classifies the current configLibrary without touching it.
func ReadState() (State, string, error) {
	dir, err := ConfigLibraryDir()
	if err != nil {
		return StateAbsent, "", err
	}
	b, err := os.ReadFile(filepath.Join(dir, "_meta.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return StateAbsent, "", nil
		}
		return StateAbsent, "", err
	}
	var m meta
	if err := json.Unmarshal(b, &m); err != nil {
		return StateAbsent, "", fmt.Errorf("parse _meta.json: %w", err)
	}
	ourFile := false
	for _, e := range m.Entries {
		if e.Name == entryName {
			ourFile = true
			break
		}
	}
	if m.AppliedID != "" && ourFile && appliedFileMatches(dir, m.AppliedID, m.Entries) {
		return StateOurs, m.AppliedID, nil
	}
	if ourFile {
		return StateOursUnapplied, m.AppliedID, nil
	}
	return StateForeign, m.AppliedID, nil
}

// appliedFileMatches reports whether the applied id is one of our
// entries (the entry list and the applied id move together).
func appliedFileMatches(dir, appliedID string, entries []metaEntry) bool {
	for _, e := range entries {
		if e.ID == appliedID && e.Name == entryName {
			return true
		}
	}
	return false
}

// ErrForeign is the refusal for a configLibrary holding a configuration
// kyacode did not write: the in-app window's work is never clobbered.
var ErrForeign = errors.New("configLibrary holds a non-kyacode configuration")

// Apply writes the two files: <id>.json first (0600, BOM-less UTF-8 -
// a BOM is reported to break the app's JSON parse), then _meta.json so
// the loader's single-file read always sees a complete pair. Refuses
// when a foreign configuration is applied.
func Apply(models []string, baseURL, helperPath string) (string, error) {
	state, applied, err := ReadState()
	if err != nil {
		return "", err
	}
	switch state {
	case StateOurs:
		// Re-on: replace our files with fresh content (new id, new model
		// list). The PREVIOUS entry file is removed first - a new id
		// would otherwise orphan it in the directory the app's window
		// lists.
		if applied != "" {
			dir0, derr := ConfigLibraryDir()
			if derr == nil {
				_ = os.Remove(filepath.Join(dir0, applied+".json"))
			}
		}
	case StateOursUnapplied, StateForeign:
		return "", fmt.Errorf("%w: applied configuration id %q is not kyacode's - use the in-app window or remove it first", ErrForeign, applied)
	}
	dir, err := ConfigLibraryDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	metaBody, id, cfgBody, err := Build(models, baseURL, helperPath)
	if err != nil {
		return "", err
	}
	entryPath := filepath.Join(dir, id+".json")
	if err := os.WriteFile(entryPath, append(cfgBody, '\n'), 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "_meta.json"), append(metaBody, '\n'), 0o600); err != nil {
		os.Remove(entryPath)
		return "", err
	}
	return id, nil
}

// Remove deletes exactly kyacode's files: our entry file and our
// _meta.json when we own the applied id. When another configuration is
// applied but our entry lingers (the in-app window took over), only our
// entry is removed and the rest of _meta.json survives. A foreign-only
// library is refused - nothing of ours is in it.
func Remove() error {
	dir, err := ConfigLibraryDir()
	if err != nil {
		return err
	}
	metaPath := filepath.Join(dir, "_meta.json")
	b, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var m meta
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("parse _meta.json: %w", err)
	}
	ours := ""
	remaining := make([]metaEntry, 0, len(m.Entries))
	for _, e := range m.Entries {
		if e.Name == entryName {
			ours = e.ID
			continue
		}
		remaining = append(remaining, e)
	}
	if ours == "" {
		return fmt.Errorf("no kyacode entry in the configLibrary - nothing of ours to remove")
	}
	if err := os.Remove(filepath.Join(dir, ours+".json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(remaining) == 0 {
		// Nothing survives: remove _meta.json too - the loader reads no
		// local config and the app returns to its sign-in screen.
		if err := os.Remove(metaPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	// Other entries survive: if ours was applied, re-point appliedId at
	// the first surviving entry so the app keeps a coherent library.
	m.Entries = remaining
	if m.AppliedID == ours {
		m.AppliedID = remaining[0].ID
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(metaPath, append(body, '\n'), 0o600)
}
