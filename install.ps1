<#
.SYNOPSIS
  Ariadne cross-platform installer for Windows (PowerShell 5.1 & PowerShell 7+).

.EXAMPLE
  irm https://raw.githubusercontent.com/daidr/Ariadne/master/install.ps1 | iex

.EXAMPLE
  .\install.ps1 -Channel preview -NoService
#>
[CmdletBinding()]
param(
  [ValidateSet('', 'stable', 'preview')]
  [string]$Channel = '',
  [string]$Version = '',
  [string]$Dir = '',
  [switch]$NoAlias,
  [switch]$NoService
)

$ErrorActionPreference = 'Stop'

# Support environment variable overrides for `irm ... | iex` usage.
if (-not $Channel -and $env:ARIADNE_CHANNEL) { $Channel = $env:ARIADNE_CHANNEL }
if (-not $Version -and $env:ARIADNE_VERSION) { $Version = $env:ARIADNE_VERSION }
if (-not $Dir -and $env:ARIADNE_INSTALL_DIR) { $Dir = $env:ARIADNE_INSTALL_DIR }
if (-not $NoAlias -and $env:ARIADNE_NO_ALIAS -eq '1') { $NoAlias = $true }
if (-not $NoService -and $env:ARIADNE_NO_SERVICE -eq '1') { $NoService = $true }

$Repo = if ($env:ARIADNE_REPO) { $env:ARIADNE_REPO } else { 'daidr/Ariadne' }
$ApiBase = if ($env:ARIADNE_GITHUB_API_URL) { $env:ARIADNE_GITHUB_API_URL.TrimEnd('/') } else { 'https://api.github.com' }
if (-not $Dir) {
  $localAppData = if ($env:LOCALAPPDATA) { $env:LOCALAPPDATA } else { Join-Path $HOME 'AppData\Local' }
  $Dir = Join-Path $localAppData 'Ariadne\bin'
}

# Ensure TLS 1.2+ on Windows PowerShell 5.1.
try {
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {
  # Ignore if already configured by newer .NET runtime.
}

# Detect Windows architecture.
$arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
if ($arch -ne [System.Runtime.InteropServices.Architecture]::X64) {
  throw "Unsupported Windows architecture: $arch. Prebuilt Windows binaries are currently published for x64 (amd64) only."
}
$AssetName = 'ariadne-windows-x64.exe'

$Headers = @{
  'Accept'     = 'application/vnd.github+json'
  'User-Agent' = 'ariadne-installer'
}
$Token = if ($env:GITHUB_TOKEN) { $env:GITHUB_TOKEN } elseif ($env:GH_TOKEN) { $env:GH_TOKEN } else { '' }
if ($Token) {
  $Headers['Authorization'] = "Bearer $Token"
}

function Select-ReleaseAsset {
  param(
    [object[]]$Releases,
    [bool]$AllowPrerelease
  )
  foreach ($rel in $Releases) {
    if ($rel.draft) { continue }
    if (-not $AllowPrerelease -and $rel.prerelease) { continue }
    foreach ($asset in $rel.assets) {
      if ($asset.name -eq $AssetName) {
        return [PSCustomObject]@{
          TagName    = [string]$rel.tag_name
          Prerelease = [bool]$rel.prerelease
          Asset      = $asset
        }
      }
    }
  }
  return $null
}

$Selected = $null
$FallbackToPreview = $false

if ($Version) {
  $tag = if ($Version.StartsWith('v')) { $Version } else { "v$Version" }
  Write-Host "Fetching Ariadne release $tag..."
  $rel = Invoke-RestMethod -Uri "$ApiBase/repos/$Repo/releases/tags/$tag" -Headers $Headers
  $Selected = Select-ReleaseAsset -Releases @($rel) -AllowPrerelease $true
} else {
  Write-Host "Checking latest Ariadne release for $AssetName..."
  $releases = Invoke-RestMethod -Uri "$ApiBase/repos/$Repo/releases?per_page=30" -Headers $Headers
  if ($Channel -eq 'stable') {
    $Selected = Select-ReleaseAsset -Releases $releases -AllowPrerelease $false
  } elseif ($Channel -eq 'preview') {
    $Selected = Select-ReleaseAsset -Releases $releases -AllowPrerelease $true
  } else {
    $Selected = Select-ReleaseAsset -Releases $releases -AllowPrerelease $false
    if (-not $Selected) {
      $Selected = Select-ReleaseAsset -Releases $releases -AllowPrerelease $true
      if ($Selected -and $Selected.Prerelease) {
        $FallbackToPreview = $true
      }
    }
  }
}

if (-not $Selected) {
  throw "No matching release asset ($AssetName) found."
}

$TagName = $Selected.TagName
$DownloadUrl = [string]$Selected.Asset.browser_download_url
$ExpectedDigest = ''
if ($Selected.Asset.PSObject.Properties['digest'] -and $Selected.Asset.digest) {
  $ExpectedDigest = [string]$Selected.Asset.digest
  if ($ExpectedDigest.StartsWith('sha256:')) {
    $ExpectedDigest = $ExpectedDigest.Substring(7)
  }
}

if ($FallbackToPreview) {
  Write-Host "No stable release is published yet; installing latest preview ($TagName)."
} else {
  Write-Host "Installing Ariadne $TagName ($AssetName)..."
}

$TempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("ariadne-install-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $TempDir -Force | Out-Null

$prevOutputEncoding = $null
try {
  $prevOutputEncoding = [Console]::OutputEncoding
  [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
} catch {
  $prevOutputEncoding = $null
}

try {
  $Staged = Join-Path $TempDir $AssetName
  # Download public release assets without Authorization headers so credentials
  # are never forwarded across cross-origin redirects to object storage.
  $dlParams = @{
    Uri     = $DownloadUrl
    OutFile = $Staged
    Headers = @{ 'User-Agent' = 'ariadne-installer' }
  }
  Invoke-WebRequest @dlParams -UseBasicParsing

  if ($ExpectedDigest) {
    $actualHash = (Get-FileHash -Path $Staged -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actualHash -ne $ExpectedDigest.ToLowerInvariant()) {
      throw "SHA-256 verification failed (expected $ExpectedDigest, got $actualHash)"
    }
    Write-Host "Verified SHA-256 checksum."
  }

  $expectedVersion = $TagName.TrimStart('v')
  $gotVersion = (& $Staged --version 2>$null | Select-Object -First 1)
  if ($gotVersion) { $gotVersion = $gotVersion.Trim() }
  if ($gotVersion -ne $expectedVersion) {
    throw "Downloaded binary reported version '$gotVersion', expected '$expectedVersion'"
  }
  & $Staged self-check | Out-Null
  if ($LASTEXITCODE -ne 0) {
    throw "Downloaded binary failed self-check"
  }

  New-Item -ItemType Directory -Path $Dir -Force | Out-Null
  $Target = Join-Path $Dir 'ariadne.exe'
  $Previous = Join-Path $Dir '.ariadne-previous.exe'
  if (Test-Path $Previous) {
    Remove-Item -Path $Previous -Force -ErrorAction SilentlyContinue
  }

  $StagedInDir = Join-Path $Dir (".ariadne-install-" + $PID + ".exe")
  Copy-Item -Path $Staged -Destination $StagedInDir -Force

  if (Test-Path $Target) {
    try {
      Move-Item -Path $Target -Destination $Previous -Force
    } catch {
      Remove-Item -Path $StagedInDir -Force -ErrorAction SilentlyContinue
      throw "Failed to stage replacement for ${Target}: $_"
    }
  }
  try {
    Move-Item -Path $StagedInDir -Destination $Target -Force
  } catch {
    if (Test-Path $Previous) {
      Move-Item -Path $Previous -Destination $Target -Force -ErrorAction SilentlyContinue
    }
    throw
  }
  if (Test-Path $Previous) {
    Remove-Item -Path $Previous -Force -ErrorAction SilentlyContinue
  }
  Write-Host "Installed binary: $Target"

  if (-not $NoAlias) {
    $AliasCmd = Join-Path $Dir 'ard.cmd'
    $aliasBody = "@echo off`r`n`"%~dp0ariadne.exe`" %*`r`n"
    [System.IO.File]::WriteAllText($AliasCmd, $aliasBody, [System.Text.Encoding]::ASCII)
    Write-Host "Created short alias: $AliasCmd"
  }

  # Idempotently update user PATH and broadcast environment change.
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $entries = @()
  if ($userPath) {
    $entries = $userPath -split ';' | Where-Object { $_ -and $_.Trim() -ne '' }
  }
  $normalizedDir = $Dir.TrimEnd('\').ToLowerInvariant()
  $hasDir = $false
  foreach ($entry in $entries) {
    if ($entry.TrimEnd('\').ToLowerInvariant() -eq $normalizedDir) {
      $hasDir = $true
      break
    }
  }
  if (-not $hasDir) {
    $newUserPath = if ($entries.Count -gt 0) { ($entries + $Dir) -join ';' } else { $Dir }
    [Environment]::SetEnvironmentVariable('Path', $newUserPath, 'User')
    if (-not ($env:Path -split ';' | Where-Object { $_.TrimEnd('\').ToLowerInvariant() -eq $normalizedDir })) {
      $env:Path = "$Dir;$env:Path"
    }
    try {
      $sig = '[DllImport("user32.dll", SetLastError=true, CharSet=CharSet.Auto)] public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint Msg, UIntPtr wParam, string lParam, uint fuFlags, uint uTimeout, out UIntPtr lpdwResult);'
      $win32 = Add-Type -MemberDefinition $sig -Name 'AriadneEnvBroadcast' -Namespace 'Win32' -PassThru -ErrorAction SilentlyContinue
      if ($win32) {
        $result = [UIntPtr]::Zero
        [void]$win32::SendMessageTimeout([IntPtr]0xffff, 0x001A, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$result)
      }
    } catch {
      # Best-effort broadcast; new shells will still pick up HKCU\Environment\Path.
    }
    Write-Host "Added $Dir to your user PATH. Open a new terminal if 'ariadne' is not recognized immediately."
  }

  if ($NoService) {
    Write-Host ""
    Write-Host "Ariadne $TagName installed. Start it anytime with:"
    Write-Host "  ariadne service install --start"
    return
  }

  Write-Host ""
  $isRunning = $false
  try {
    $statusJson = & $Target service status --json 2>$null | Out-String
    if ($statusJson) {
      $parsed = $statusJson | ConvertFrom-Json
      if ($parsed.running) { $isRunning = $true }
    }
  } catch {
    $isRunning = $false
  }

  if ($isRunning) {
    Write-Host "Detected a running Ariadne service; performing a safe restart..."
    & $Target service restart
  } else {
    Write-Host "Registering and starting the Ariadne background service..."
    & $Target service install --start
  }
  if ($LASTEXITCODE -ne 0) {
    $exitCode = $LASTEXITCODE
    Write-Warning "Ariadne binary was installed at '$Target', but background service setup failed (exit code $exitCode)."
    Write-Host "You can retry or inspect the failure with:"
    if ($isRunning) {
      Write-Host "  & `"$Target`" service restart"
    } else {
      Write-Host "  & `"$Target`" service install --start"
      Write-Host "  & `"$Target`" service install --no-autostart --start   # if startup registration is blocked by policy"
    }
    Write-Host "  & `"$Target`" service logs"
    throw "Ariadne service setup exited with code $exitCode"
  }
} finally {
  if ($null -ne $prevOutputEncoding) {
    try {
      [Console]::OutputEncoding = $prevOutputEncoding
    } catch {
      # Ignore if console handle is unavailable.
    }
  }
  if (Test-Path $TempDir) {
    Remove-Item -Path $TempDir -Recurse -Force -ErrorAction SilentlyContinue
  }
}
