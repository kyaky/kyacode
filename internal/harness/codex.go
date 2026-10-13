package harness

import (
	"fmt"
	"path/filepath"
	"strings"

	"kyacode/internal/harness/sandbox"
)

// codexPrefix is the one sandbox anchor codexSpec.SandboxPrefix declares
// and the local CarrierBuild's sandbox.New call uses — two readers, one
// constant (the pairings and anti-drift rationale live on the Spec
// field's doc in registry.go).
const codexPrefix = "codex"

// Codex wires OpenAI Codex CLI via an isolated CODEX_HOME under
// ~/.kyacode/codex-<pid>-<rand> (per-user dir, never world-shared /tmp — a
// residual dir would hold the real key). The sandbox handle reclaims the
// dir when the launch returns.
var codexSpec = Spec{
	Name:          "codex",
	Description:   "OpenAI Codex CLI (isolated CODEX_HOME, no disk writes to ~/.codex)",
	Wiring:        WiringSandbox,
	SandboxPrefix: codexPrefix,
	// Catalog carrier, and still RequiresModel: the /model picker serves
	// the ACTIVE model catalog, and for a custom provider without an
	// explicit one that is codex's bundled OpenAI list — every row 404s
	// against the gateway, so the picker was unusable. The carrier
	// handover lets the wiring render the gateway's own rows as the
	// catalog (models.json + the model_catalog_json pin) so the picker
	// lists — and switches among — real gateway models mid-session. The
	// typed model stays required as the ENTRY point: the gateway
	// publishes no capability metadata, so the catalog's first row is no
	// guarantee of a working responses route, and a model-less launch
	// would silently start on whatever sorted first. The entry model
	// rides the -c pin and heads the picker rows.
	RequiresModel: true,
	Find: func() (string, bool) {
		return findIn("codex", nil)
	},
	Install: func() (bin string, args []string) {
		return "npm", []string{"install", "-g", "@openai/codex@latest"}
	},
	CarrierBuild: func(ep Endpoint, model string, cat Catalog) (Wire, error) {
		return buildSandbox(codexPrefix, sandbox.Dual, ep.Token, func(sb *sandbox.Sandbox) ([]string, []string, error) {
			// The picker's models must carry codex's own base-instruction
			// template: a catalog model without model_messages launches
			// sessions with EMPTY system instructions (no bundled
			// fallback exists), and the template ships only inside the
			// codex binary — so the entry SHAPE is harvested from the
			// installed codex itself, in lockstep with whatever version
			// is installed. resolveBin has already run by the time the
			// launch boundary hands the carrier its catalog, so the
			// binary exists here; the same findIn probe Find used
			// locates it again. Any harvest or render failure degrades
			// to no catalog file: the launch proceeds exactly as before
			// the catalog existed.
			catalogPath := ""
			if entry, ok := codexCatalogEntryHarvest(sb.Root()); ok {
				if body, ok := codexCatalogJSON(cat.Rows(), model, entry); ok {
					if err := sb.Write("models.json", body); err != nil {
						return nil, nil, err
					}
					catalogPath = filepath.Join(sb.Root(), "models.json")
				}
			}
			pairs, err := codexWiringPairs(ep, model, catalogPath)
			if err != nil {
				return nil, nil, err
			}
			// config.toml carries the same facts at the user layer — the
			// human-readable record of the wiring; the argv pins repeat
			// them at the runtime layer, which outranks every file layer
			// (see codexWiringPairs for why the repetition is load-bearing
			// and not drift).
			if err := sb.Write("config.toml", []byte(codexConfigToml(pairs))); err != nil {
				return nil, nil, err
			}
			// Key channel Dual: the key VALUE rides in auth.json and the
			// process env (sb.Credentials()), while the wiring references
			// the env var by name (wire_api=responses authenticates via
			// provider env_key). The CLI's own subcommand still comes
			// from Launch's passthrough; the `-c` pins precede it.
			auth := `{"OPENAI_API_KEY":` + quoteJSON(ep.Token) + `}`
			if err := sb.Write("auth.json", []byte(auth)); err != nil {
				return nil, nil, err
			}
			return append([]string{"CODEX_HOME=" + sb.Root()}, sb.Credentials()...), codexRuntimeArgs(pairs), nil
		})
	},
}

// configPair is one wiring fact: a TOML key and its value VERBATIM — the
// value carries its own quoting (`"disabled"`, bare `false`,
// %-quoted strings) so both renderings emit exactly the validated bytes.
type configPair struct {
	key   string
	value string
}

// codexWiringPairs is the ONE source of every fact the codex wiring
// injects. Since codex 0.162 each fact must land in two places that must
// never drift: the sandbox config.toml (the user config layer) and the
// child argv as `-c key=value` runtime overrides. The config loader
// stacks file layers ABOVE the user layer — `${PWD}/config.toml`, then
// `.codex/config.toml` in every parent directory up to the filesystem
// root, then the git root's — and enables them the moment the operator
// answers the TUI's trust prompt for that folder. A launch from the home
// directory therefore reads the user's real ~/.codex/config.toml as a
// "project" layer, and its `model` (whatever the user has configured
// there) replaced the wired gateway model before the first request
// (rollout-verified;
// `codex exec` never noticed because it never prompts for trust). The
// `-c` runtime layer outranks every file layer, so the pins hold for
// trusted projects, untrusted ones, and bare launches alike. The
// investigation behind these pins covered bisect transcripts, the
// upstream timeline and issue numbers, and the gateway-side glm-5.3
// responses-route gap.
//
// catalogPath non-empty adds the model_catalog_json pair (both
// renderings) — the file the /model picker serves; see codex_catalog.go
// for why the file exists and what it carries.
func codexWiringPairs(ep Endpoint, model string, catalogPath string) ([]configPair, error) {
	baseURL, err := validateTOMLString(ep.OpenAIBase())
	if err != nil {
		return nil, fmt.Errorf("gateway URL not embeddable in codex config: %w", err)
	}
	if model != "" {
		if _, err := validateTOMLString(model); err != nil {
			return nil, fmt.Errorf("model name not embeddable in codex config: %w", err)
		}
	}
	var pairs []configPair
	if model != "" {
		pairs = append(pairs, configPair{"model", fmt.Sprintf("%q", model)})
	}
	if catalogPath != "" {
		pairs = append(pairs, configPair{"model_catalog_json", fmt.Sprintf("%q", catalogPath)})
	}
	return append(pairs,
		configPair{"model_provider", `"kya"`},
		// The throwaway home carries only the gateway key — there is no
		// ChatGPT login to fall back to, and codex 0.162's TUI gates on
		// login state. Pinning the login method to "api" (the
		// ForcedLoginMethod enum's other arm is "chatgpt") makes the
		// never-prompted property explicit instead of emergent — DeepSeek's
		// official codex recipe pins the same key for the same reason.
		configPair{"forced_login_method", `"api"`},
		// codex ≥0.162 defaults the root-level web_search switch to
		// "cached" and injects a hosted `web_search` tool for any model
		// it has no metadata for (every gateway id — the gateway serves
		// glm/qwen families codex has never heard of). The gateway
		// refuses those requests ("offline search is not supported"),
		// which surfaces as a launch failure that reads like an auth
		// problem while the key is fine. The pre-0.162 gate
		// ([tools] web_search = false) is parsed then deliberately
		// discarded upstream, so the root enum key
		// (disabled|cached|indexed|live) is the only live switch.
		// "disabled" is unconditional: the gateway cannot serve
		// OpenAI-hosted search for any model, and the other three values
		// all inject the tool.
		configPair{"web_search", `"disabled"`},
		// codex 0.162's daemon_auto_start feature (default on) spawns a
		// background app-server daemon that outlives the CLI, and the
		// daemon resolves provider credentials from its own env/auth
		// snapshot — the class upstream tracks as #52806/#50877/#49651.
		// A persistent daemon is also architecturally wrong for a
		// throwaway home: everything it would serve dies with the
		// sandbox.
		configPair{"features.daemon_auto_start", "false"},
		// The plugins feature (default on) runs a marketplace git clone
		// into CODEX_HOME/.tmp at startup that can outlive the CLI and
		// hold a FETCH_HEAD handle past child exit, so Cleanup's retry
		// window loses the race and the key survives on disk. The flag
		// is broad — ALL plugin functionality, not just the marketplace
		// clone — and that is the deliberate shape: a throwaway home
		// persists nothing between launches, so plugin loading dies with
		// the sandbox anyway; keeping it on only buys a per-launch
		// re-clone and the cleanup race.
		configPair{"features.plugins", "false"},
		// The provider table, again as dotted runtime keys: a trusted
		// project layer could otherwise redefine [model_providers.kya]
		// and walk the gateway key out to a different base_url.
		configPair{"model_providers.kya.name", `"KYA Gateway"`},
		configPair{"model_providers.kya.base_url", fmt.Sprintf("%q", baseURL)},
		configPair{"model_providers.kya.wire_api", `"responses"`},
		configPair{"model_providers.kya.env_key", fmt.Sprintf("%q", sandbox.KeyEnvName)},
	), nil
}

// codexConfigToml renders the pairs as the user-layer config.toml: root
// keys first, then the provider table (TOML spec: root keys after a
// [table] header silently become table children — the
// writeCodexNamedProfileConfig layout, which codex's own parser follows).
func codexConfigToml(pairs []configPair) string {
	var root, table []string
	for _, p := range pairs {
		if name, ok := strings.CutPrefix(p.key, "model_providers.kya."); ok {
			table = append(table, name+" = "+p.value)
		} else {
			root = append(root, p.key+" = "+p.value)
		}
	}
	return strings.Join(root, "\n") + "\n\n[model_providers.kya]\n" + strings.Join(table, "\n") + "\n"
}

// codexRuntimeArgs renders the pairs as `-c key=value` runtime overrides —
// the layer that outranks every file layer, trusted projects included.
func codexRuntimeArgs(pairs []configPair) []string {
	args := make([]string, 0, len(pairs)*2)
	for _, p := range pairs {
		args = append(args, "-c", p.key+"="+p.value)
	}
	return args
}

// validateTOMLString rejects strings that would break out of a
// double-quoted TOML basic string: CR/LF (line injection into config.toml),
// NUL, and double quotes (key injection). Tab is legal in TOML strings and
// is not rejected.
func validateTOMLString(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n\x00\"") {
		return "", fmt.Errorf("contains CR/LF/NUL/quote")
	}
	return s, nil
}

// quoteJSON escapes s for embedding in a JSON string literal.
// Handles C0 control chars (< 0x20), C1 control chars (0x80-0x9F),
// double quote, and backslash — the full set JSON requires escaping.
func quoteJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || (r >= 0x80 && r <= 0x9f) {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
