# kyacode installer - Windows form. Runs under:
#
#   irm https://kyaky.ai/kyacode/install.ps1 | iex
#
# Non-interactive by construction. Refusals are `throw`, never `exit` -
# an `exit` inside piped `iex` would kill the user's shell. Under
# `irm | iex` arguments cannot be passed, so $env:KYA_VERSION is the only
# pin channel there; a downloaded copy also takes -Target.
#
# Environment:
#   KYA_VERSION                  pin a tag (default latest)
#   KYACODE_INSTALL_DIR          override the install dir
#                                (default %LOCALAPPDATA%\Programs\kyacode)
#   KYACODE_SKIP_PATH_PERSIST    set to skip the user-PATH write (for
#                                scripted/package-managed environments)

param([string]$Target)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# The Asset base: the dist directory the kyaky.ai site deployment
# serves. One place to flip - the cross-platform guidance below derives
# from it, so the printed URL can never drift from the download origin.
$AssetBase = 'https://kyaky.ai/kyacode'

# --- windows-only guard ---
# $env:OS is Windows_NT on every Windows PowerShell (5.1 and 7) and
# unset on Linux/macOS pwsh. Do NOT probe an $IsWindows variable here:
# variable names are case-insensitive, so a probe via Get-Variable
# collides with any user-defined $isWindows in scope (this installer
# defines one itself) and reads back its own value.
$isWinHost = ($env:OS -eq 'Windows_NT')
if (-not $isWinHost) {
	throw "kyacode: this is the Windows installer; on POSIX run: curl -fsSL $AssetBase/install.sh | sh"
}

# --- module path floor for PS 5.1 ---
# When Windows PowerShell 5.1 is spawned from a PowerShell 7 session (CI
# shells, some terminals), it inherits PS7's PSModulePath and cannot
# autoload its own built-in modules (Get-FileHash dies with
# CommandNotFoundException). Pin the PS5 system module path whenever the
# script runs under 5.x; PS 7 is left untouched.
if ($PSVersionTable.PSVersion.Major -le 5) {
	$env:PSModulePath = "$env:windir\System32\WindowsPowerShell\v1.0\Modules"
}

# --- version pin: explicit -Target wins over env; 'latest' or vX.Y.Z ---
if (-not $PSBoundParameters.ContainsKey('Target') -and $env:KYA_VERSION) { $Target = $env:KYA_VERSION }
if (-not $Target) { $Target = 'latest' }
$sub = ''
if ($Target -ne 'latest') {
	if ($Target -notmatch '^v?\d+(\.\d+)*$' -or $Target -like '*..*') {
		throw "kyacode: bad version tag '$Target' (need latest or vX.Y.Z)"
	}
	if (-not $Target.StartsWith('v')) { $Target = 'v' + $Target }
	$sub = "/$Target"
}

# --- architecture: refuse before any network call ---
switch ($env:PROCESSOR_ARCHITECTURE) {
	'AMD64' { $asset = 'kyacode-windows-amd64.exe' }
	'ARM64' { throw "kyacode: unsupported architecture ARM64 (this build ships windows amd64)" }
	default { throw "kyacode: unsupported architecture '$($env:PROCESSOR_ARCHITECTURE)' (this build ships windows amd64)" }
}

# TLS floor for PS 5.1 on older builds; a no-op where TLS 1.2 is default.
try {
	[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch { }

$installDir = if ($env:KYACODE_INSTALL_DIR) { $env:KYACODE_INSTALL_DIR }
else { Join-Path $env:LOCALAPPDATA 'Programs\kyacode' }
$final = Join-Path $installDir 'kyacode.exe'

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("kyacode-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force $tmp | Out-Null
try {
	$binUrl = "$AssetBase$sub/$asset"
	Write-Host "kyacode: downloading $asset ..."
	try {
		Invoke-WebRequest -UseBasicParsing -Uri $binUrl -OutFile (Join-Path $tmp $asset)
		Invoke-WebRequest -UseBasicParsing -Uri "$binUrl.sha256" -OutFile (Join-Path $tmp "$asset.sha256")
	} catch {
		throw "kyacode: download failed ($binUrl): $($_.Exception.Message)"
	}

	$want = ((Get-Content (Join-Path $tmp "$asset.sha256") -TotalCount 1) -split '\s+')[0]
	$got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $asset)).Hash.ToLower()
	if ($want -ne $got) {
		throw "kyacode: checksum mismatch - refusing to install"
	}

	New-Item -ItemType Directory -Force $installDir | Out-Null
	# stage beside the final name, then rename: the swap into place is
	# atomic; a running kyacode.exe holds the file and fails the move
	# loudly instead of leaving a half-replaced install.
	$staged = Join-Path $installDir ('.kyacode.tmp-' + [guid]::NewGuid().ToString('N') + '.exe')
	Copy-Item (Join-Path $tmp $asset) $staged -Force
	try {
		Move-Item -Force $staged $final
	} catch {
		throw "kyacode: could not replace kyacode.exe - close running kyacode and re-run ($($_.Exception.Message))"
	}

	# --- user PATH: registry-backed, idempotent, Machine scope untouched ---
	if ($env:KYACODE_SKIP_PATH_PERSIST) {
		Write-Host "kyacode: skipping user-PATH persistence (KYACODE_SKIP_PATH_PERSIST is set)"
	} else {
		$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
		if (($userPath -split ';') -notcontains $installDir) {
			$newPath = if ($userPath) { "$userPath;$installDir" } else { $installDir }
			try {
				[Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
				Write-Host "kyacode: added $installDir to the user PATH (new shells pick it up)"
			} catch {
				Write-Host "kyacode: WARNING could not persist PATH; add it manually: [Environment]::SetEnvironmentVariable('Path', `"$newPath`", 'User')"
			}
		}
	}
	if (($env:Path -split ';') -notcontains $installDir) { $env:Path = "$env:Path;$installDir" }

	$cmd = Get-Command kyacode -ErrorAction SilentlyContinue
	if ($cmd -and ($cmd.Source -ne $final)) {
		Write-Host "kyacode: WARNING an existing kyacode resolves to $($cmd.Source) - it may keep winning PATH resolution over $final"
	}

	Write-Host "kyacode: installed $final"
	try {
		& $final version
	} catch {
		Write-Host 'kyacode: WARNING the installed binary did not run cleanly'
	}
	Write-Host 'kyacode: next steps:'
	Write-Host '  kyacode auth     # paste your gateway key'
	Write-Host '  kyacode doctor   # pre-flight checks'
	Write-Host '  kyacode launch <harness> <model>'
} finally {
	Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
