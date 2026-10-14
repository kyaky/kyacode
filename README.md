# kyacode

Model traffic never has to leave your own perimeter. kyacode wires eleven
coding harnesses (Claude Code, Codex, opencode, Qwen Code and seven more) to
a self-hosted KYA inference gateway (the default instance runs at kyaky.ai),
so prompts, completions and spawn-time wiring stay inside the perimeter you
trust, under your own data governance, fast, with no metered token billing.

A single cgo-free Go binary takes your gateway key (stored locally), installs
in one line on macOS, Linux and Windows, and injects the wiring (environment
variables and throwaway config files) at spawn time, so your real harness
configuration is never touched. Data sovereignty is a property of the setup,
not a policy promise.

<!-- The release badge is static and updated by hand (README churn must not
     trigger release cycles); `kyacode version` is the authoritative check
     for an installed binary's version. -->
[![release](https://img.shields.io/badge/release-v0.1.1-2ea44f?style=flat-square)](https://github.com/kyaky/kyacode/releases)
[![Go](https://img.shields.io/badge/Go-1.27-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev/dl/)
[![license](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat-square)](LICENSE)

<p align="center">
  <a href="https://buymeacoffee.com/kyaky" target="_blank"><img src="https://cdn.buymeacoffee.com/buttons/v2/default-yellow.png" alt="Buy Me A Coffee" style="height: 60px !important;width: 217px !important;"></a>
</p>

## Contents

- [Requirements](#requirements)
- [Installing the binary](#installing-the-binary) (one-liners, version pinning, updating, removing, verify)
- [Quick start](#quick-start)
- [Commands](#commands)
- [Harness wiring](#harness-wiring) (including 1M context with Claude Code)
- [Storage and security boundaries](#storage-and-security-boundaries)
- [Troubleshooting](#troubleshooting)
- [Fallback: fetch from the GitHub release](#fallback-fetch-from-the-github-release)
- [Build from source](#build-from-source)
- [Repository layout](#repository-layout)
- [Provenance](#provenance)
- [Acknowledgements](#acknowledgements)
- [Issues and security](#issues-and-security)
- [License](#license)

## Requirements

- One supported harness CLI. A missing harness is auto-installed on first
  `launch` where an official installer exists: claude and opencode use their
  own install scripts, the rest of the installable set installs via
  `npm install -g` (so Node.js/npm must be on `PATH`); see [Commands](#commands).
- A valid KYA gateway key (issued with your gateway deployment).

## Installing the binary

One line per platform. POSIX shells (macOS, Linux):

```bash
curl -fsSL https://kyaky.ai/kyacode/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://kyaky.ai/kyacode/install.ps1 | iex
```

Shipped builds: macOS and Linux in x86_64 and arm64; Windows in x86_64 only
(the installer refuses Windows-on-ARM).

From cmd.exe, run: `powershell -NoProfile -Command "irm https://kyaky.ai/kyacode/install.ps1 | iex"`.
Pasting the POSIX one-liner into Git Bash on Windows refuses by design and
prints the Windows command. Both URLs are the canonical script forms served
by the KYA inference host; there is no redirect layer to keep alive.

Every platform installs the same command: plain `kyacode` (`kyacode.exe` on
Windows). The binary lands in `%LOCALAPPDATA%\Programs\kyacode` on Windows
with the user PATH registered, and in `~/.local/bin` on POSIX, with a
paste-ready `export PATH=...` hint printed if that directory is not on
`PATH`. The installer never elevates, never prompts, and never touches rc
files. The POSIX installer refuses to run as root (it installs into your own
home) and needs `curl` on `PATH`. Scripted installs: `KYACODE_INSTALL_DIR`
relocates the install directory on both platforms, and setting
`KYACODE_SKIP_PATH_PERSIST` skips the Windows user-PATH registration.

Pin a version with `KYA_VERSION` or a positional argument:

```bash
curl -fsSL https://kyaky.ai/kyacode/install.sh | KYA_VERSION=v0.1.0 sh
curl -fsSL https://kyaky.ai/kyacode/install.sh | sh -s v0.1.0
```

(The intuitive `KYA_VERSION=v0.1.0 curl ... | sh` does nothing: the prefix
binds the variable to `curl` only, and `sh` never sees it.)

Every install downloads the binary and its `.sha256` sidecar, verifies
before replacing, and refuses, leaving the working binary intact, on any
download or checksum failure. The one failure that prints nothing and exits
0 with nothing installed is covered in [Troubleshooting](#troubleshooting).

### Updating

Re-run the same one-liner: it overwrites the installed binary in place, and
the `latest` assets in the dist directory are always the newest build. A
pinned `KYA_VERSION` install is equally repeatable.

### Removing kyacode

Run `kyacode unauth` to remove the stored key. Delete the binary
(`~/.local/bin/kyacode` on POSIX, `%LOCALAPPDATA%\Programs\kyacode` on
Windows, plus the user-PATH entry if you want it gone) and remove
`~/.kyacode` for the leftover `config.json`. Per-launch sandboxes are
cleaned on exit and swept by `kyacode status`, so they need no action.

### Verify the install

Either route, verify what you ended up running:

```bash
kyacode version
```

A tagged-release install prints its semver (stamped at build time); a build
from source prints `v0.0.0-dev` and says so.

## Quick start

> [!NOTE]
> These commands assume `kyacode` is already on `PATH`; if it is not, start
> with [Installing the binary](#installing-the-binary).

```bash
kyacode auth                            # paste your gateway key (stored 0600 on unix)
kyacode doctor                          # pre-flight: five gateway probes
kyacode launch claude qwen3.8-27b-nvfp4
```

The gateway key comes with your gateway deployment; kyacode itself does not
issue keys.

Model ids are gateway-specific: run `kyacode models` to list yours. Note
that `kyacode models` lists every id the gateway serves, including
embedding, rerank and speech families no harness can chat with; the launch
wiring filters those out of the harness pickers, so pass a chat model id.

No gateway setup is needed for the default KYA deployment: `doctor`, `models`
and `launch` target `https://kyaky.ai`. Point somewhere else with
`kyacode gateway <url>`, or print the current setting with bare
`kyacode gateway`.

Pointing kyacode at your own instance? One `https://` base must answer all
of: `POST /v1/messages` (Anthropic shape), `POST /v1/chat/completions` and
`POST /v1/responses` (OpenAI shapes), and `GET /v1/models` returning an
OpenAI-style `{"data":[{"id":...}]}` list. Every call carries the same key
as a `Bearer` `Authorization` header, and a 3xx answer is refused outright
(a redirect hop would carry the key). `kyacode doctor` checks a candidate
base URL against all of these in one run. A trailing `/v1` on a pasted URL
is accepted and stripped; the stored form is canonically without it.

## Commands

| command                                          | purpose                                                                                           |
|--------------------------------------------------|---------------------------------------------------------------------------------------------------|
| `kyacode auth`                                   | store your gateway key (hidden prompt on a tty; `--stdin` forces the raw read; mode 0600 on unix) |
| `kyacode unauth`                                 | remove the stored key                                                                             |
| `kyacode gateway [url]`                          | show or set the gateway base URL (`https://` required; see `KYA_GATEWAY_INSECURE`)                |
| `kyacode doctor`                                 | pre-flight: five probes (three entry protocols, a warning-level beta-header check, and the model listing) |
| `kyacode status`                                 | key / gateway / harness overview; runs the orphan sweep                                           |
| `kyacode models`                                 | list gateway models (10s timeout)                                                                 |
| `kyacode launch <harness> [model] [-- extra args...]` | spawn a harness against the gateway                                                          |
| `kyacode version`                                | print the build's version (tagged releases stamp semver)                                          |
| `kyacode help`                                   | usage (`-h` / `--help`)                                                                           |

Extra args after `--` are passed through verbatim to the harness:

```bash
kyacode launch claude <model> -- --continue
```

Before `--`, the first argument is taken as the model only when it does not
start with `-`; a leading flag goes to the harness instead. Any other extra
argument before `--` is refused: harness flags belong after `--`. The
single-model harnesses (see [Harness wiring](#harness-wiring)) refuse a
launch with no model at all:
`<harness> requires a model: kyacode launch <harness> <model> [-- extra args...]`

A missing harness is auto-installed through its official installer, after a
`y/N` confirmation, except `hermes`, `openclaw` and `qwen`, which you must
install yourself.

The eleven harnesses:

| name     | product                      | install channel                                            |
|----------|------------------------------|------------------------------------------------------------|
| claude   | Anthropic Claude Code        | official installer (claude.ai script / PowerShell)         |
| opencode | opencode                     | official installer (curl on POSIX, npm on Windows)         |
| codex    | OpenAI Codex CLI             | `npm i -g @openai/codex`                                   |
| cline    | Cline                        | `npm i -g cline`                                           |
| droid    | Droid (Factory)              | `npm i -g droid`                                           |
| dsh      | DeepSeek Harness             | `npm i -g @deepseek-ai/dsh`                                |
| omp      | Oh My Pi                     | `npm i -g oh-my-pi`                                        |
| pi       | Pi coding agent              | `npm i -g @earendil-works/pi-coding-agent`                 |
| qwen     | Qwen Code CLI                | manual (see the Qwen Code project)                         |
| hermes   | Hermes (Nous Research)       | manual (see the upstream project)                          |
| openclaw | OpenClaw                     | manual (see the upstream project)                          |

### Exit codes

| exit | meaning                                                                                                                                               |
|------|-------------------------------------------------------------------------------------------------------------------------------------------------------|
| `0`  | the subcommand succeeded                                                                                                                              |
| `1`  | any failed subcommand: refused command shape (extra/unexpected arguments), key/gateway problems, redirect refusals, failed pre-flight, failed launch  |
| `2`  | unknown subcommand or no subcommand at all (usage-shaped misuse)                                                                                      |

Integrations that only need success/failure can treat everything non-zero as
failure; `2` additionally tells you the invocation itself was malformed.

An interrupted launch also exits 1 once the escalation windows expire
(stderr banner `interrupt: forcing exit`, with no further `error:` line); a
child that finished cleanly during the interrupt grace still exits 0.

### Environment variables

The environment variables kyacode itself reads (`KYA_API_KEY`, which kyacode
exports into harness sandboxes, is covered under [Harness wiring](#harness-wiring)):

| variable               | effect                                                                |
|------------------------|-----------------------------------------------------------------------|
| `KYA_GATEWAY`          | override the gateway base URL (wins over config.json)                 |
| `KYA_GATEWAY_INSECURE` | non-empty value allows plaintext `http://` to loopback hosts only     |

## Harness wiring

Wiring is injected at spawn time: into the launched process tree, or into a
throwaway sandbox that your normal harness sessions never read. Rows marked
**yes** are materialised in a per-launch directory
`~/.kyacode/<harness>-<pid>-<random>/` (0700) that is removed when the launch
returns, including on Ctrl-C/SIGTERM/SIGHUP. The first three harnesses are
pure env injection (they leave no file behind), so they are listed first.

| harness  | wiring                                                 | persisted? |
|----------|--------------------------------------------------------|------------|
| claude   | env: `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN`      | no         |
| opencode | env: `OPENCODE_CONFIG_CONTENT` (inline JSON)            | no         |
| qwen     | env: `OPENAI_BASE_URL` + `OPENAI_API_KEY` + `OPENAI_MODEL` | no      |
| codex    | sandbox: `CODEX_HOME` (`config.toml` + `auth.json` + `models.json`) | yes        |
| cline    | sandbox: `CLINE_DATA_DIR` (`providers.json`)            | yes        |
| droid    | sandbox: HOME (`.factory/settings.json`)                | yes        |
| pi       | sandbox: HOME (`.pi/agent/models.json`)                 | yes        |
| openclaw | sandbox: HOME (`.openclaw/openclaw.json`)               | yes        |
| hermes   | sandbox: `HERMES_HOME` (`config.yaml`)                  | yes        |
| omp      | sandbox: `PI_CODING_AGENT_DIR` (`models.yml`)           | yes        |
| dsh      | sandbox: settings.yaml + patch (key by env reference)   | yes        |

Five sandbox rows (codex, droid, dsh, openclaw and pi) additionally export
`KYA_API_KEY`; the harness config files either reference it (codex
`env_key`, dsh `apiKeyEnv`) or carry a copy.
Rows that replace `HOME` (droid, openclaw and pi) redirect all of the
harness's runtime state (sessions, caches) into the sandbox, which is
discarded when the launch ends.

claude, codex, droid, dsh, omp, openclaw, opencode and pi fetch
the gateway's full model catalog at launch, so the harness's own picker can
select among gateway models. droid, dsh, omp, openclaw, opencode and pi list
every served id; codex lists the chat-capable ids; claude renders a bounded
four-slot alias picker.

A model you pass becomes the default;
with no model given, droid, dsh, omp, openclaw and pi pin the first real
id of a fetched catalog as the default
(claude, codex and opencode never write a default: claude and codex
refuse a model-less launch outright, so the typed model rides the
spawn-time wiring, and opencode's own picker decides at startup, a choice
deliberately held until its boot behavior is re-verified).
If the catalog is unavailable (a transient gateway outage) and no model was
passed, no default is written anywhere and the catalog degrades to the
`kya-model` placeholder row; what each harness then does at startup is its
own fallback behavior.

omp, openclaw, opencode and pi take a BARE model id: they scope it
themselves with the kya/ provider prefix, so a provider-scoped argument is
refused at launch. The remaining harnesses wire the model their own way:
droid takes a bare id on the command line (its CLI refuses scoped
custom-model ids), dsh carries it in the sandbox config, and claude, cline,
codex, hermes and qwen wire a single model only. Check `kyacode models` for
the current list.

### 1M context with Claude Code

Claude Code sizes a model's context window from the **model id string**, so
a gateway alias needs a hint (verified on Claude Code 2.1.261). The rules:

| model id as passed to `kyacode launch`                 | window Claude Code assumes                        |
|--------------------------------------------------------|---------------------------------------------------|
| contains `[1m]`                                        | 1,000,000                                         |
| unresolvable, no `[1m]`, does not start with `claude-` | 200,000                                           |
| starts with `claude-`, no `[1m]`                       | Claude Code's own native resolution (not measured here) |

For the third row and any other unmeasured id, check the assumed window
in-session with `/context`.

So pass the suffix through: it is Claude Code's own convention, **stripped
before the request leaves for the provider**, so the gateway never needs a
model literally named `glm-5.3-flash-nvfp4[1m]`:

```bash
kyacode launch claude 'glm-5.3-flash-nvfp4[1m]'
```

Two caveats:

- The suffix is read per variable: the `opus`/`sonnet` aliases and `--model`
  are each read on their own, so for a gateway id keep the suffix on the one
  you select.
- Claude Code only *assumes* the window. The model must actually serve it
  (`--max-model-len` on the inference side), otherwise a long prompt fails
  with a prompt-too-long error rather than compacting.

`[1m]` is Claude Code only; it means nothing to Codex, opencode, Qwen Code
or the other harnesses. To raise Qwen Code's window, set
`generationConfig.contextWindowSize` on the model's `modelProviders` entry
in `~/.qwen/settings.json`.

## Storage and security boundaries

| location                               | mode | lifetime            |
|----------------------------------------|------|---------------------|
| `~/.kyacode/token`                     | 0600 | until `unauth`      |
| `~/.kyacode/config.json`               | 0600 | manual              |
| `~/.kyacode/<harness>-<pid>-<random>/` | 0700 | per launch          |
| child process env (env rows)           | n/a  | dies with the child |

On Windows, `~` above is your user profile: the store lives at
`%USERPROFILE%\.kyacode`.

- `token` is written through an exclusive temp file created at 0600 (a umask
  can only strip bits from that, never add them back) and chmod'd 0600 on
  the open handle before the rename, so the key is never briefly
  world-readable. The 0600 is a unix guarantee: on Windows Go's `os.Chmod`
  maps the mode to the read-only attribute only, and NTFS ACLs govern access.
- `config.json` is written 0600 and re-chmod'd 0600 on every save, so even a
  pre-existing lax-mode file is tightened rather than tolerated. Same unix
  scoping as the token above.
- `kyacode status` shows only a short preview of the stored key (first 3 and
  last 4 characters; keys shorter than 16 characters show no preview at
  all), never the full value.
- `kyacode models` sends the key nowhere when the stored gateway cannot be
  read or validated (a config parse error, a file-shaped `~/.kyacode`, an
  invalid `KYA_GATEWAY` override): the fetch fails instead of silently
  falling back to the default gateway.
- Wiring is inherited by grandchildren of the harness (accepted cost of the
  no-proxy design: the harness talks straight to the gateway, with nothing
  local in between).
- Nothing personal belongs under `~/.kyacode`. The orphan sweep removes any
  directory matching the sandbox shape `<harness>-<pid>-<random>` (older
  than 24 h), so user-named directories sharing a harness prefix
  (`codex-archive`, `pi-notes`) are never touched, but keep personal data
  out of this tree anyway. The age bound is the only guard: there is
  deliberately no PID-liveness check, and sandbox mtimes are written once at
  build time and never refresh, so a single launch running longer than 24 h
  can have its sandbox swept by a concurrent `kyacode status`.
- `kyacode status` may print `WARNING: orphan sweep incomplete` when the
  `~/.kyacode` scan itself fails (e.g. it is not a readable directory); the
  command still succeeds.

> [!WARNING]
> `SIGKILL` (or a second Ctrl-C) kills `kyacode` before its deferred cleanup
> runs, orphaning the sandbox and env wiring: no handler can intercept
> SIGKILL, and the second Ctrl-C resets the signal disposition so it kills
> immediately. On Windows the first Ctrl-C ends the launch exactly like
> POSIX: the interrupt ladder (kyacode's staged Ctrl-C handling) delivers a
> process-group break to the harness tree (including the `.cmd` shim and its
> node/bun descendants), then force-kills the whole tree, which also catches
> grandchildren that daemonized away from the group. Run `kyacode status`
> later; it sweeps orphans.

## Troubleshooting

- Any gateway command (`doctor`, `models`, `launch`) fails with
  ``no token stored (run `kyacode auth`)``: run `kyacode auth` and paste the
  key; `kyacode status` shows whether a key is stored.
- The harness launches but every model call fails with an auth or model
  error: the spawn wiring only points the harness at the gateway. Run
  `kyacode doctor` (a `401`/`403` row means the key was refused, so re-run
  `kyacode auth` with a fresh key) and confirm the id in `kyacode models`.
- The one-line install prints nothing and exits 0 with nothing installed:
  when the dist directory does not yet serve the requested script, curl
  fails and the pipe feeds `sh` an empty script, which exits 0 silently (the
  site's copy is behind the GitHub release). Retry later, or fetch the
  binary from the
  [releases page](https://github.com/kyaky/kyacode/releases) directly.
- A manual fetch from the GitHub release (the
  [Fallback](#fallback-fetch-from-the-github-release) section) returns 404
  when the release or the asset is missing; check the
  [releases page](https://github.com/kyaky/kyacode/releases).
- A launch or probe fails with
  `gateway redirected to <url> (refused: the hop would carry the key)`: the
  URL answers with a redirect (a CDN or proxy front); point
  `kyacode gateway <url>` at the origin that answers directly.
- `doctor` probes fail: check the `gateway:` line doctor prints first. Fix a
  wrong base URL with `kyacode gateway <url>`; a `401`/`403` row means the
  key was refused, so re-run `kyacode auth` with a fresh key; anything else
  may be a transient gateway outage.
- `doctor` shows 404s on green rows: expected. Four of the five probes
  deliberately send a nonexistent model, and a `404` row reading
  `probe miss confirmed` is the pass evidence for those rows. A confirmed
  probe miss proves the route parses requests and consults its models,
  never that inference, streaming, or tool calls work (doctor prints that
  honesty note on every run).
- Leftover `~/.kyacode/<harness>-<pid>-<random>/` directories: `kyacode
  status` sweeps them; see
  [Storage and security boundaries](#storage-and-security-boundaries).
- `launch` refuses with `<harness> is not installed; install it first`: the
  harnesses without an official installer (see the auto-install note under
  [Commands](#commands)) must be installed by you first; `kyacode status`
  shows which harness binaries were found.
- A hand-edit that breaks `~/.kyacode/config.json` fails every
  gateway-reading command (`doctor`, `status`, `models`, `launch`) with
  `parse <path>: ...`; there is no silent fallback, and `KYA_GATEWAY` does
  not bypass it (the parse error fires before the override).
  `kyacode gateway <url>` repairs the setting without reading the old file.
  If `~/.kyacode` itself is a regular file rather than a directory, the
  error reports it: remove or rename that file, then re-run `kyacode auth`
  and `kyacode gateway <url>`.

## Fallback: fetch from the GitHub release

The install one-liners at the top are all you need. If you prefer to fetch
the binary from the GitHub release yourself (an air-gapped prefetch, a
mirror-averse environment), release assets follow the pattern
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
    || { rm -rf "$t"; echo "kyacode: checksum mismatch - refusing to install" >&2; return 1; }
  mkdir -p ~/.local/bin
  install -m 0755 "$t/kyacode-$os-$arch" ~/.local/bin/kyacode
  rm -rf "$t"
}
fetch_kyacode && echo "installed to ~/.local/bin/kyacode"
```

Every stage gates the install: a download error (404, truncation) or a
checksum mismatch fails loudly and exits non-zero **before `install`
runs**, and the scratch directory is removed either way. The arch case
refuses unmapped `uname -m` values up front instead of requesting a wrong
asset name.

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
installs them; for the verified install, use the fetch above.

## Build from source

Go ≥ 1.27. There is intentionally no `make` and no build script: the whole
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

A release build additionally stamps the tag as its version: release.yml adds
`-X main.Version=<tag>` to the same `-ldflags`, so a binary built exactly
per the recipe above reports `v0.0.0-dev` under `kyacode version`.

## Repository layout

```text
cmd/kyacode/         CLI entrypoint (dispatch and subcommands; the ladder needs no CLI code)
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

Decision records and internal analysis notes are intentionally not tracked
in git; `CONTEXT.md`, the project's vocabulary of record, is tracked at the
repo root.

## Provenance

kyacode is also a demonstration: local AI coding agents on commercial and
open-source harnesses carrying a software project's full lifecycle
(research, development, maintenance, release) through GitHub, with all model
inference on the maintainer's own gateway under local data governance and
sovereignty. 'Local' covers model inference; the harness CLIs keep their own
upstream update and telemetry channels. The checkable record: the commit
history, the CI and release workflows, and the hygiene tests that pin the
docs to the code.

Development ran on kyacode itself: in the private-era version numbering (the
public releases start at v0.1.0), versions through v4.2 were driven by the
[opencode](https://github.com/sst/opencode) and
[qwen-code](https://github.com/QwenLM/qwen-code) harnesses, every version
after v4.2 by [Claude Code](https://github.com/anthropics/claude-code), on
the same local gateway models (`glm-5.3-flash-nvfp4`, `qwen3.8-flash-next`
and `qwen3.8-27b-nvfp4`), all of it built entirely by AI coding agents,
every change supervised, reviewed and accepted by a human maintainer.

## Acknowledgements

kyacode's design took shape against Restech Technology Services, UNSW's
self-hosted high-performance inference gateway, and this project owes that
gateway a direct debt: its free model access is what made the work possible
at all. kyacode is an independent project and is not affiliated with UNSW;
the acknowledgement is where the lineage ends.

## Issues and security

Bug reports and feature requests go to
[GitHub issues](https://github.com/kyaky/kyacode/issues). For anything
security-sensitive, please reach out privately first (see
[kyaky.ai](https://kyaky.ai)) rather than opening a public issue.

## License

Apache License 2.0. See [LICENSE](LICENSE).
