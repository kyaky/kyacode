# kyacode

kyacode launches external coding-agent CLIs against the KYA gateway with the
user's own key. All wiring is injected at spawn time and is never written to
the harness's real config.

## Language

### Launching a harness

**Harness**:
An external coding-agent CLI that kyacode launches (claude, codex, dsh, hermes, omp, droid, pi, openclaw, cline, opencode, qwen).
_Avoid_: agent, CLI, tool

**Runner**:
The launch wiring for one supported harness inside kyacode. A runner is either env-wired or sandbox-wired, and that class is declared on its Spec — a load-bearing fact that determines where the key channel may legally live.
_Avoid_: adapter, plugin, harness wrapper

**Spec**:
The wiring record one runner registers: its wiring class (env or sandbox), model requirement, composer shape, and sandbox prefix, plus the functions that carry the launch. Launch dispatches on the declared class; a capability declared in data but absent as code — or never readable because the class disagrees — is a registration error, never a silently lost one at launch.
_Avoid_: descriptor, config struct, capability interface

**Wiring**:
The spawn-time configuration kyacode hands a harness so it talks to the gateway with the user's key: injected process env and/or a throwaway sandbox's config files.
_Avoid_: integration, setup, configuration

**Wire**:
The value one launcher returns to the launch boundary: the sandbox root, the child-env rows, the runner's own argv, and the idempotent reclaim. Per-launch state is returned — never parked in package state or smuggled through a pseudo env var, and never re-derived by scanning the rows that came back.
_Avoid_: build result, launch output

**Root dir**:
The ~/.kyacode directory holding the token, config.json, and the per-launch sandboxes. Its path has exactly one owner (internal/rootdir's pure resolution); each consumer applies its own creation policy on top, and the split is stated at the rule in rootdir's package doc.
_Avoid_: home dir, the config dir (when the whole root is meant)

**Sandbox**:
The per-launch throwaway directory kyacode materialises to carry a sandbox-wired harness's config. It holds the real key, is removed when the launch returns, and reclamation must never fail silently.
_Avoid_: workdir, temp profile, home override

**Key channel**:
How the real gateway key reaches a harness's config: written into a sandbox file (file value), referenced in a file by the env var's NAME (env reference), or exported as process env (env value). A file may carry the key's value or a name-reference — never a name as a value, which would be sent verbatim as the credential.
_Avoid_: auth method, credential path, key mode

**Launch**:
One kyacode invocation that spawns a single harness child process with its wiring; it ends when the child exits, after which the sandbox (if any) is reclaimed.
_Avoid_: run, session

### Interrupts and residue

**Interrupt ladder**:
The escalation internal/ladder runs when the operator interrupts: a soft rung to the harness tree, a TERM rung, a bounded window whose expiry fires the tree force, a second bounded grace for the child's own exit and cleanup — whose expiry fires the force again (re-reading the published record: H-3) plus the sandbox reclaim, then the module's Exit contract. The CLI owns only signal arming; the ladder owns publication, escalation, and exit honesty, and is testable in-process at fast configurable timings.
_Avoid_: signal handler, cleanup routine, supervision shell

**Handoff**:
The joint child+cleanup record one launch publishes through (the atomic successor of hook pairs). An interrupt either sees the whole record or none of it — never a live sandbox paired with a nil child; every escalation window reads the same record, and the late-Start re-read is what decides the force target.
_Avoid_: hooks, launch observers, slot pair

**Rung**:
One step of the interrupt ladder, in escalation order.
_Avoid_: step, level

**Orphan sweep**:
The reclamation of key-bearing sandboxes left behind past deferred cleanup (SIGKILL, second-Ctrl-C exit). Runs with `kyacode status` only, under a 24h age bound and exact name-shape match, with no PID-liveness check by design.
_Avoid_: GC, janitor, cleanup

### The gateway

**Gateway**:
The KYA inference gateway, which natively speaks the anthropic, openai-chat, and responses wire protocols plus the model list.
_Avoid_: backend, LLM provider

**Gateway module**:
The one keyed transport to the gateway (internal/gateway): the /v1 base grammar (the join and the canonical strip — both halves cross-pinned together), the Bearer-only header shape, the typed redirect refusal, the single timeout and the two read budgets (whole-catalog for the list fetch, bounded for a probe), and the parsed /v1/models fetch. Doctor preflights with the very fetch a launch rides, so their verdicts cannot disagree; callers compose it, never their own keyed HTTP client.
_Avoid_: keyhttp, HTTP layer, client helper

**Gateway base**:
The gateway URL kyacode stores and hands to consumers, canonically WITHOUT the /v1 suffix; every wire consumer appends the version suffix itself.
_Avoid_: endpoint, base URL (when the /v1 form is meant)

**Entry protocol**:
The dialect the gateway speaks natively at one of its routes — anthropic, openai-chat, or responses — including the shape its errors arrive in. The same refusal (an unknown model, say) is told differently per entry protocol, and a pre-flight answer is classified by the protocol of the route that gave it. Distinct from the wire protocols (what the gateway speaks as a whole): the entry protocol is the per-route dialect, error shape included.
_Avoid_: error format, dialect (bare), wire shape (when the error-shape is meant)

**Probe miss**:
The expected outcome of a negative pre-flight probe: the gateway answers that the probe's deliberately nonexistent model does not exist, naming that model in its entry-protocol error. A confirmed probe miss proves the route parses requests and consults its models — never that inference, streaming, or tool calls work. An answer that names no probe model cannot be told apart from a route that does not exist at all.
_Avoid_: 404 (as the noun for the outcome), not-found (bare), failed probe (for an expected miss)

**Provider scope**:
The gateway's provider key that namespaces served model ids (the "kya/<id>" form). Some harnesses accept only the scoped form, some only bare ids; the launch asks its Spec, and the Spec refuses a provider-scoped CLI argument to a scoped composer.
_Avoid_: namespace, prefix (alone)

**Model catalog**:
The merged model directory a harness config carries: the CLI-chosen model first, then every gateway model, sorted.
_Avoid_: model list, picker

**Catalog carrier**:
A runner whose config embeds the model catalog fetched from the gateway's model list at launch. Declared on its Spec by the carrier channel it sets — CarrierBuild (sandbox-wired) or CarrierEnv (env-wired); a carrier never fetches itself. Whether a carrier may also declare RequiresModel is a carrier-body property, not a declaration check: its wiring must serve every catalog row it renders into its picker (codex and claude hold the pairing — one provider and one key carry every row). Membership is not placement: a carrier that renders its entry model through its own spawn-time wiring (claude's ANTHROPIC_MODEL pin, codex's config `model`) consumes no default pin from the Catalog decision.
_Avoid_: model-aware runner, dynamic runner

**Catalog decision**:
The per-launch resolved value the launch boundary hands a Catalog carrier: the merged rows plus the default-pin verdict rendered in both forms (bare and provider-scoped). Resolved once by Resolve — only for a carrier — and opaque to carriers: the Sentinel row and the no-pin-on-degrade pairing are decided there, never re-derived from the rows a carrier received; the pin's FORM stays placement knowledge, read from how each carrier consumes the decision (pi, dsh and droid pin bare; omp and openclaw pin scoped; claude and codex read no pin — their entry model rides their own spawn-time wiring; opencode reads no pin — held).
_Avoid_: fetched catalog (bare), catalog value, dossier

**Sentinel row**:
The deliberate placeholder row kept in every degraded model catalog. It is the only selectable row an auto-selecting harness can land on, so it keeps their implicit startup selection on the gateway; removal is a standing rejected design.
_Avoid_: fake model, stub entry

**Degrade**:
The launch-time condition in which the gateway's model list is unreachable: the catalog carries the sentinel row alone and no default pin is written.
_Avoid_: fallback, offline mode

### Distribution

**Asset source**:
The kyaky.ai Pages project's public/kyacode/ tree, served at https://kyaky.ai/kyacode/: flat names (install.sh, install.ps1, the binaries and their checksum sidecars) for latest, v<tag>/ directories for pins. The repository's releases feed Sync; a release is also the fallback install target (the primary route is the asset source's canonical install URLs).
_Avoid_: download server, CDN, mirror, origin (bare)

**Sync**:
The kyaky.ai-side ingestion of one release's assets (scripts/sync-kyacode.mjs there, runnable locally and via that repo's sync-kyacode workflow): anonymous download with an authenticated gh fallback, every sidecar verified BEFORE anything is placed, exact-set check in both directions, flat + v<tag>/ placement, pin dirs pruned to the newest five, then a commit and push; a local push triggers the existing Pages deploy, while the sync-kyacode workflow explicitly dispatches it because GITHUB_TOKEN pushes do not trigger workflows. A re-sync of the same tag is idempotent.
_Avoid_: publish (the maintainer-run ssh placement this replaced), deploy (bare), release upload

**Installer**:
The script a user's shell or PowerShell executes to fetch, verify and place the binary — POSIX and Windows forms of one contract. It runs non-interactive by construction (it executes under a piped curl), never elevates privileges, and treats a failed verification as a refused install.
_Avoid_: setup wizard, installer package

**Entrance**:
The short install URL /kyacode, a 302 to /kyacode/install.sh declared in the Pages project's public/_redirects — one line in the site repo, not a second artifact kept in sync by hand. The original short-domain entrance layer was dropped in the 2026-10-14 rebrand; this lighter form took its place the same day.
_Avoid_: short URL (bare), landing page
