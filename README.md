# kyacode

Model traffic never has to leave your own perimeter. kyacode wires eleven
coding harnesses — Claude Code, Codex, opencode, Qwen Code and seven more —
to a self-hosted KYA inference gateway (the default instance runs at
kyaky.ai), so prompts, completions and spawn-time wiring stay inside the
perimeter you trust, under your own data governance — fast, with no metered
token billing. A single cgo-free Go binary takes your gateway key (stored
locally), installs in one line on macOS, Linux and Windows, and injects the
wiring at spawn time — your real harness configuration is never touched.
Data sovereignty as a property of the setup, not a policy promise.

<!-- The release badge is static and updated by hand (README churn must not
     trigger release cycles); `kyacode version` is the authoritative check
     for an installed binary's version. -->
[![release](https://img.shields.io/badge/release-v4.6.1-2ea44f?style=flat-square)](https://github.com/kyaky/kyacode/releases)
[![Go](https://img.shields.io/badge/Go-1.27-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev/dl/)
[![license](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat-square)](LICENSE)

## Requirements

- One supported harness — `launch` auto-installs the ones with an official
  installer; the rest you install yourself (see [Commands](#commands)).
- A valid KYA gateway key — see [kyaky.ai](https://kyaky.ai)

## Installing the binary

One line per platform. POSIX shells (macOS, Linux):

```bash
curl -fsSL https://kyaky.ai/kyacode/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://kyaky.ai/kyacode/install.ps1 | iex
```

(from cmd.exe: `powershell -NoProfile -Command "irm https://kyaky.ai/kyacode/install.ps1 | iex"`)

(Pasting the POSIX one-liner into Git Bash on Windows refuses by design — it
prints the Windows command.)

(Both URLs are the canonical script forms served by the KYA inference host —
there is no redirect layer to keep alive.)

Every platform installs the same command — plain `kyacode` (`kyacode.exe`
on Windows). The binary lands in `%LOCALAPPDATA%\Programs\kyacode` on
Windows with the user PATH registered, and in `~/.local/bin` on POSIX, with
a paste-ready `export PATH=...` hint printed if that directory is not on
`PATH`. The installer never elevates, never prompts, and never touches rc
files. Scripted installs: `KYACODE_INSTALL_DIR` relocates the install
directory on both platforms, and setting `KYACODE_SKIP_PATH_PERSIST` skips
the Windows user-PATH registration.

Pin a version with `KYA_VERSION` or a positional argument:

```bash
curl -fsSL https://kyaky.ai/kyacode/install.sh | KYA_VERSION=v4.6.1 sh
curl -fsSL https://kyaky.ai/kyacode/install.sh | sh -s v4.6.1
```

(The intuitive `KYA_VERSION=v4.5.0 curl ... | sh` does nothing: the prefix
binds the variable to `curl` only — `sh` never sees it.)

Every install downloads the binary and its `.sha256` sidecar, verifies
before replacing, and refuses — leaving the working binary intact — on
any download or checksum failure. The one failure that prints nothing and
exits 0 with nothing installed is described in the first
[Troubleshooting](#troubleshooting) bullet.

### Updating

Re-run the same one-liner: it overwrites the installed binary in place,
and the `latest` assets in the dist directory are always the newest
build. A pinned `KYA_VERSION` install is equally repeatable.

### Fallback: fetch from the GitHub release

The install one-liners above are all you need. If you prefer to fetch the
binary from the GitHub release yourself — an air-gapped prefetch, a mirror-
averse environment — release assets follow the pattern
`kyacode-<goos>-<goarch>[.exe]`, each with a companion `.sha256` sidecar.

Windows: fetch `kyacode-windows-amd64.exe` from the dist directory
(`https://kyaky.ai/kyacode/`) or download it from the
[releases page](https://github.com/kyaky/kyacode/releases).

A verified fetch for POSIX shells:

```bash
fetch_kyacode() {
  local os arch base t
  os=$(uname -s | tr '[:upper:]' '[:lower:]'); arch=$(uname -m)
  case "$arch" in
    x86_64)        arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) echo "kyacode: unsupported architecture '$arch' (need x86_64 or arm64/aarch64)" >&2; return 1 ;;
  esac
  base="https://github.com/kyaky/kyacode/releases/latest/download"
  t=$(mktemp -d) || return 1
  curl -fL -o "$t/kyacode-$os-$arch"        "$base/kyacode-$os-$arch" \
    || { rm -rf "$t"; echo "kyacode: binary download failed" >&2; return 1; }
  curl -fL -o "$t/kyacode-$os-$arch.sha256" "$base/kyacode-$os-$arch.sha256" \
    || { rm -rf "$t"; echo "kyacode: checksum file download failed" >&2; return 1; }
  (cd "$t" && { sha256sum --check "kyacode-$os-$arch.sha256" 2>/dev/null \
                 || shasum -a 256 --check "kyacode-$os-$arch.sha256"; }) \
    || { rm -rf "$t"; echo "kyacode: checksum mismatch — refusing to install" >&2; return 1; }
  mkdir -p ~/.local/bin
  install -m 0755 "$t/kyacode-$os-$arch" ~/.local/bin/kyacode
  rm -rf "$t"
}
fetch_kyacode && echo "installed to ~/.local/bin/kyacode"
```

Every stage gates the install: a download error (404, truncation) or a
checksum mismatch fails loudly and exits non-zero **before
`install` runs**, and the scratch directory is removed either way. The
arch case refuses unmapped `uname -m` values up front instead of
requesting a wrong asset name.

Or with `gh` only:

```bash
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64)        arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "kyacode: unsupported architecture '$(uname -m)' (need x86_64 or arm64/aarch64)" >&2; false ;;
esac && gh release download --repo kyaky/kyacode --pattern "kyacode-$os-$arch*"
```

(the unmatched case ends in `false`, so `gh` never runs with an
unset/widened glob; the exit status carries the failure).

`gh` alone stops at the download: it drops `kyacode-$os-$arch` and its
`.sha256` sidecar into the working directory and neither verifies nor
installs them — for the verified install, use the fetch above.

### Verify the install

Either route, verify what you ended up running:

```bash
kyacode version
```

A tagged-release install prints its semver (stamped at build time); a
build from source prints `v0.0.0-dev` and says so.

## Quick start

> [!NOTE]
> These commands assume `kyacode` is already on `PATH`; if it is not,
> start with [Installing the binary](#installing-the-binary).

```bash
kyacode auth                            # paste your gateway key (stored 0600 on unix)
kyacode doctor                          # pre-flight: five gateway probes
kyacode launch claude qwen3.8-27b-nvfp4
```

Need a gateway key? See [kyaky.ai](https://kyaky.ai).

Model ids are gateway-specific — run `kyacode models` to list yours.

No gateway setup is needed for the default KYA deployment — `doctor`,
`models` and `launch` target `https://kyaky.ai`.
Point somewhere else with `kyacode gateway <url>` (a trailing `/v1` on a
pasted URL is accepted and stripped — the stored form is canonically
without it), or print the current setting with bare `kyacode gateway`.

## Commands

| command                                          | purpose                                                                                           |
|--------------------------------------------------|---------------------------------------------------------------------------------------------------|
| `kyacode auth`                                   | store your gateway key (hidden prompt on a tty; `--stdin` forces the raw read; mode 0600 on unix) |
| `kyacode unauth`                                 | remove the stored key                                                                             |
| `kyacode gateway [url]`                          | show or set the gateway base URL (`https://` required; see `KYA_GATEWAY_INSECURE`)                |
| `kyacode doctor`                                 | pre-flight: five probes — three entry protocols, warning-level beta-header check, model listing   |
| `kyacode status`                                 | key / gateway / harness overview; runs the orphan sweep                                           |
| `kyacode models`                                 | list gateway models (10s timeout)                                                                 |
| `kyacode launch <harness> [model] [-- extra args...]` | spawn a harness against the gateway                                                          |
| `kyacode version`                                | print the build's version (tagged releases stamp semver)                                          |
| `kyacode help`                                   | usage (`-h` / `--help`)                                                                           |

Extra args after `--` are passed through verbatim to the harness:

```bash
kyacode launch claude <model> -- --continue
```

Before `--`, the first argument is the model only if it does not start
with `-` — a leading flag goes to the harness instead, and any other
extra argument before `--` is refused: harness flags belong after `--`.
The single-model harnesses (see [Harness wiring](#harness-wiring)) refuse a
launch with no model at all:
`<harness> requires a model: kyacode launch <harness> <model> [-- extra args...]`

A missing harness is auto-installed through its official installer, after a
`y/N` confirmation — except `hermes`, `openclaw` and `qwen`, which you must
install yourself.

### Exit codes

| exit | meaning                                                                                                                                               |
|------|-------------------------------------------------------------------------------------------------------------------------------------------------------|
| `0`  | the subcommand succeeded                                                                                                                              |
| `1`  | any failed subcommand — refused command shape (extra/unexpected arguments), key/gateway problems, redirect refusals, failed pre-flight, failed launch |
| `2`  | unknown subcommand or no subcommand at all (usage-shaped misuse)                                                                                      |

Integrations that only need success/failure can treat everything non-zero
as failure; `2` additionally tells you the invocation itself was malformed.

An interrupted launch also exits 1 once the escalation windows expire
(stderr banner `interrupt: forcing exit`, with no further `error:` line);
a child that finished cleanly during the interrupt grace still exits 0.

### Environment variables

The environment variables kyacode itself reads (`KYA_API_KEY`, which kyacode
exports into harness sandboxes, is covered under [Harness wiring](#harness-wiring)):

| variable               | effect                                                                |
|------------------------|-----------------------------------------------------------------------|
| `KYA_GATEWAY`          | override the gateway base URL (wins over config.json)                 |
| `KYA_GATEWAY_INSECURE` | non-empty value allows plaintext `http://` to loopback hosts only     |

## Harness wiring

Wiring is injected at spawn time — into the launched process tree, or into a
throwaway sandbox that your normal harness sessions never read. Rows marked
**yes** are materialised in a per-launch directory
`~/.kyacode/<harness>-<pid>-<random>/` (0700) that is removed when the launch
returns, including on Ctrl-C/SIGTERM/SIGHUP. The first three harnesses are
pure env injection — they leave no file behind — so they are listed first.

| harness  | wiring                                                 | persisted? |
|----------|--------------------------------------------------------|------------|
| claude   | env: `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN`      | no         |
| opencode | env: `OPENCODE_CONFIG_CONTENT` (inline JSON)            | no         |
| qwen     | env: `OPENAI_BASE_URL` + `OPENAI_API_KEY`               | no         |
| codex    | sandbox: `CODEX_HOME` (`config.toml` + `auth.json` + `models.json`) | yes        |
| cline    | sandbox: `CLINE_DATA_DIR` (`providers.json`)            | yes        |
| droid    | sandbox: HOME (`.factory/settings.json`)                | yes        |
| pi       | sandbox: HOME (`.pi/agent/models.json`)                 | yes        |
| openclaw | sandbox: HOME (`.openclaw/openclaw.json`)               | yes        |
| hermes   | sandbox: `HERMES_HOME` (`config.yaml`)                  | yes        |
| omp      | sandbox: `PI_CODING_AGENT_DIR` (`models.yml`)           | yes        |
| dsh      | sandbox: settings.yaml + patch (key by env reference)   | yes        |

Five sandbox rows — codex, droid, dsh, openclaw and pi — additionally export
`KYA_API_KEY`; the harness config files either reference it
(codex `env_key`, dsh `apiKeyEnv`) or carry a copy.
Rows that replace `HOME` (droid, openclaw, pi) redirect all of the
harness's runtime state — sessions, caches — into the sandbox, which is
discarded when the launch ends.

claude, codex, droid, dsh, omp, openclaw, opencode and pi fetch
the gateway's full model catalog at launch, so every model is selectable
in the harness's own picker.

A model you pass becomes the default;
with no model given, droid, dsh, omp, openclaw and pi pin the first real
id of a fetched catalog as the default
(claude, codex and opencode never write a default — claude and codex
refuse a model-less launch outright and the typed model rides the
spawn-time wiring; opencode's is held pending source evidence on its
boot pick, its picker decides).
If the catalog is unavailable (transient gateway outage) and no model
was passed, no default is written anywhere — what each harness then does
at startup is its own fallback behavior — and the catalog degrades to
the `kya-model` placeholder row.

omp, openclaw, opencode and pi take a BARE model id: they scope it
themselves with the kya/ provider prefix, so a provider-scoped argument
is refused at launch. The other five harnesses (claude, cline, codex,
hermes, qwen) wire a single model only — check `kyacode models` for the
current list.

## 1M context with Claude Code

Claude Code sizes a model's context window from the **model id string**, so a
gateway alias needs a hint (verified on Claude Code 2.1.261). The rules:

| model id as passed to `kyacode launch`                          | window Claude Code assumes |
|-----------------------------------------------------------------|----------------------------|
| contains `[1m]`                                                 | 1 000 000                  |
| unresolvable, no `[1m]`, does not start with `claude-`          | 200 000                    |

Ids outside these two rows were not measured — for any such id, check
the assumed window in-session with `/context`.

So pass the suffix through — it is Claude Code's own convention, **stripped
before the request leaves for the provider**, so the gateway never needs a
model literally named `glm-5.3-flash-nvfp4[1m]`:

```bash
kyacode launch claude 'glm-5.3-flash-nvfp4[1m]'
```

Two caveats:

- The suffix is read per variable — the `opus`/`sonnet` aliases and
  `--model` are each read on their own — so for a gateway id, keep the
  suffix on the one you select.
- Claude Code only *assumes* the window. The model must actually serve it
  (`--max-model-len` on the inference side), otherwise a long prompt fails
  with a prompt-too-long error rather than compacting.

`[1m]` is Claude Code only — it means nothing to Codex, opencode, Qwen Code or
the other harnesses. To raise Qwen Code's window, set
`generationConfig.contextWindowSize` on the model's `modelProviders` entry in
`~/.qwen/settings.json`.

## Storage and security boundaries

| location                               | mode | lifetime            |
|----------------------------------------|------|---------------------|
| `~/.kyacode/token`                     | 0600 | until `unauth`      |
| `~/.kyacode/config.json`               | 0600 | manual              |
| `~/.kyacode/<harness>-<pid>-<random>/` | 0700 | per launch          |
| child process env (env rows)           | n/a  | dies with the child |

- `token` is written through an exclusive temp file created at 0600 (a
  umask can only strip bits from that, never add them back) and chmod'd
  0600 on the open handle before the rename, so the key is never briefly
  world-readable. The 0600 is a unix guarantee: on Windows Go's
  `os.Chmod` maps the mode to the read-only attribute only, and NTFS
  ACLs govern access.
- `config.json` is written 0600 — re-chmod'd 0600 on every save, so even
  a pre-existing lax-mode file is tightened rather than tolerated. Same
  unix scoping as the token above.
- `kyacode status` shows only a short preview of the stored key (first 3
  and last 4 characters; keys shorter than 16 characters show no preview
  at all), never the full value.
- `kyacode models` sends the key nowhere when the gateway cannot be resolved
  (no silent fallback to the default gateway).
- Wiring is inherited by grandchildren of the harness (accepted cost of the
  no-proxy design).
- Nothing personal belongs under `~/.kyacode` — the orphan sweep removes
  any directory matching the sandbox shape
  `<harness>-<pid>-<random>` (older than 24 h), so user-named directories
  sharing a harness prefix (`codex-archive`, `pi-notes`) are never touched,
  but keep personal data out of this tree anyway. The age bound is the
  only guard — there is deliberately no PID-liveness check, and sandbox
  mtimes are written once at build time and never refresh — so a single
  launch running longer than 24 h can have its sandbox swept by a
  concurrent `kyacode status`.
- `kyacode status` may print `WARNING: orphan sweep incomplete` when the
  `~/.kyacode` scan itself fails (e.g. it is not a readable directory) —
  the command still succeeds.

> [!WARNING]
> `SIGKILL` (or a second Ctrl-C) kills `kyacode` before its deferred
> cleanup runs, orphaning the sandbox and env wiring — no handler can
> intercept SIGKILL, and the second Ctrl-C resets the signal disposition
> so it kills immediately. On Windows the first Ctrl-C ends the launch
> exactly like POSIX: the ladder delivers a process-group break to the
> harness tree (including the `.cmd` shim and its node/bun descendants),
> then force-kills the whole tree — which also catches grandchildren that
> daemonized away from the group. Run `kyacode status` later — it sweeps
> orphans.

## Troubleshooting

- The one-line install prints nothing and exits 0 with nothing installed —
  the pipe swallowed a missing file: the served dist directory is missing
  an asset (the site's copy is behind the GitHub release). Retry later,
  or fetch the binary from the
  [releases page](https://github.com/kyaky/kyacode/releases) directly.
- Download returns 404 from the GitHub fallback — the release or the asset
  is missing; check the
  [releases page](https://github.com/kyaky/kyacode/releases) (see the
  [Fallback](#fallback-fetch-from-the-github-release) section).
- `doctor` probes fail — check the `gateway:` line doctor prints first:
  fix a wrong base URL with `kyacode gateway <url>`; a `401`/`403` row
  means the key was refused — re-run `kyacode auth` with a fresh key;
  anything else may be a transient gateway outage.
- `doctor` shows 404s on green rows — expected: four of the five probes
  deliberately send a nonexistent model, and `404 — probe miss confirmed`
  is the pass evidence for those rows. A confirmed probe miss proves the
  route parses requests and consults its models — never that inference,
  streaming, or tool calls work (doctor prints that honesty note on every
  run).
- Leftover `~/.kyacode/<harness>-<pid>-<random>/` directories — `kyacode status`
  sweeps sandboxes older than 24 h and prints exactly which paths it removed.
- `launch` refuses with `<harness> is not installed; install it first` —
  the harnesses without an official installer (see the auto-install note
  under [Commands](#commands)) must be installed by you first; `kyacode status`
  shows which harness binaries were found.
- A hand-edit that breaks `~/.kyacode/config.json` fails every
  gateway-reading command (`doctor`, `status`, `models`, `launch`) with
  `parse <path>: ...` — there is no silent fallback, and `KYA_GATEWAY`
  does not bypass it (the parse error fires before the override).
  `kyacode gateway <url>` repairs the setting without reading the old
  file. If `~/.kyacode` itself is a regular file rather than a directory,
  the error reports it — remove or rename that file, then re-run
  `kyacode auth` and `kyacode gateway <url>`.

## Build from source

Go ≥ 1.27. There is intentionally no `make` and no build script — the whole
program is a single module with no cgo.

```bash
# local build (darwin/linux/windows, cgo disabled, stripped, trim-pathed)
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/kyacode ./cmd/kyacode
```

Cross-compiling for a release target follows the same recipe with `GOOS` /
`GOARCH` set, e.g.:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/kyacode-linux-arm64 ./cmd/kyacode
```

The full target matrix is in
[`.github/workflows/release.yml`](.github/workflows/release.yml).

A release build additionally stamps the tag as its version — release.yml
adds `-X main.Version=<tag>` to the same `-ldflags` — so a binary built
exactly per the recipe above reports `v0.0.0-dev` under
`kyacode version`.

## Repository layout

```text
cmd/kyacode/         CLI entrypoint (dispatch, subcommands — the ladder needs no CLI code)
internal/config/     gateway URL + token path + persistence
internal/rootdir/    the ~/.kyacode name's one owner (pure path resolution; consumers apply their own creation policy)
internal/doctor/     pre-flight probes over the gateway module (entry protocols + compat + catalog)
internal/harness/    runner registry: 11 harnesses (env-only + sandbox); sandbox materialisation and the <harness>-<pid>-<random> grammar (internal/harness/sandbox)
internal/hygiene/    CI/README self-gates: workflow pins, README prose pins, executable install snippets
internal/gateway/    the one keyed transport to the gateway: /v1 join, Bearer shape, typed redirect refusal, /v1/models fetch
internal/ladder/     the interrupt ladder: published handoff, escalation rungs, exit contract
internal/procctl/    process-control primitives (group config, graceful rung, TERM rung, tree force)
scripts/             the installer pair (install.sh, install.ps1)
```

Internal development docs are intentionally not tracked in git.

## Provenance

kyacode is also a demonstration: local AI coding agents on commercial and
open-source harnesses carrying a software project's full lifecycle —
research, development, maintenance, release — through GitHub, all model
inference on the maintainer's own gateway, under local data governance
and sovereignty — 'local' covers model inference; the harness CLIs keep
their own upstream update and telemetry channels. The checkable record:
the commit history, the CI and release workflows, the hygiene tests that
pin the docs to the code.

Development ran on kyacode itself: versions through v4.2 were driven by
the [opencode](https://github.com/sst/opencode) and
[qwen-code](https://github.com/QwenLM/qwen-code) harnesses, every version
after v4.2 by [Claude Code](https://github.com/anthropics/claude-code), on
the same local gateway models — `glm-5.3-flash-nvfp4`, `qwen3.8-flash-next`
and `qwen3.8-27b-nvfp4` — all of it built entirely by AI coding agents,
every change supervised, reviewed and accepted by a human maintainer.

## Acknowledgements

kyacode's design took shape against Restech Technology Services, UNSW's
self-hosted high-performance inference gateway, and this project owes
that gateway a direct debt: its free model access is what made the work
possible at all. kyacode is an independent project and is not affiliated
with UNSW — the acknowledgement is where the lineage ends.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
