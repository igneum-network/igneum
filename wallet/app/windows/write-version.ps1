# Writes wallet-version.h from the workspace's Cargo.toml (wallet/Cargo.toml, two levels up), so "Igneum Wallet.exe"
# carries the cut's version in its file metadata. Run by BUILD-WALLET-APP.bat before rc.exe.
$ErrorActionPreference = 'Stop'
$toml = Join-Path $PSScriptRoot '..\..\Cargo.toml'
$t = Get-Content $toml -Raw
if ($t -notmatch '(?m)^version\s*=\s*"(\d+)\.(\d+)\.(\d+)') { throw "no version line in $toml" }
$v = "$($Matches[1]).$($Matches[2]).$($Matches[3])"; $rc = "$($Matches[1]),$($Matches[2]),$($Matches[3]),0"
$lines = @(
  '// written by write-version.ps1 from wallet/Cargo.toml: the version of "Igneum Wallet.exe" (the window host)',
  '#ifndef IGNEUM_HOST_VERSION_H',
  '#define IGNEUM_HOST_VERSION_H',
  "#define IGNEUM_HOST_VERSION_STR `"$v`"",
  "#define IGNEUM_HOST_VERSION_RC  $rc",
  '#endif'
)
Set-Content -Path (Join-Path $PSScriptRoot 'wallet-version.h') -Value $lines -Encoding ASCII
Write-Host "[build] version $v"
