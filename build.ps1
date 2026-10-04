# Builds "dist\Encore.exe" and the NSIS installer.
#   .\build.ps1 -Version 0.2.0
# Requires Go, go-winres (go install github.com/tc-hib/go-winres@latest)
# and NSIS (makensis) on PATH.
param(
  [Parameter(Mandatory = $true)][string]$Version,
  [switch]$SkipInstaller
)
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

go test ./...
if ($LASTEXITCODE) { throw 'tests failed' }

go-winres make --in winres/winres.json --product-version $Version --file-version $Version
if ($LASTEXITCODE) { throw 'go-winres failed' }

New-Item -ItemType Directory -Force dist | Out-Null
$ldflags = "-H windowsgui -s -w -X main.version=$Version -X main.updatesEnabled=true"
# The app's own Last.fm API account (optional; users can add their own).
if ($env:LASTFM_API_KEY -and $env:LASTFM_SHARED_SECRET) {
  $ldflags += " -X main.lastfmAPIKey=$env:LASTFM_API_KEY -X main.lastfmAppSecret=$env:LASTFM_SHARED_SECRET"
  Write-Host 'Including the Last.fm API account'
} else {
  Write-Host 'No LASTFM_API_KEY/LASTFM_SHARED_SECRET: users add their own Last.fm API account'
}
go build -trimpath -ldflags $ldflags -o "dist\Encore.exe" .
if ($LASTEXITCODE) { throw 'go build failed' }

if (-not $SkipInstaller) {
  makensis /V2 "/DVERSION=$Version" installer\encore.nsi
  if ($LASTEXITCODE) { throw 'makensis failed' }
  $installer = Get-Item "dist\Encore-Setup-$Version.exe"

  # latest.yml lets Electron 0.1.x installs update themselves to this build.
  $bytes = [IO.File]::ReadAllBytes($installer.FullName)
  $sha512 = [Convert]::ToBase64String([Security.Cryptography.SHA512]::Create().ComputeHash($bytes))
  $date = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ss.fffZ')
  @"
version: $Version
files:
  - url: $($installer.Name)
    sha512: $sha512
    size: $($bytes.Length)
path: $($installer.Name)
sha512: $sha512
releaseDate: '$date'
"@ | Set-Content -Encoding utf8 dist\latest.yml
  Write-Host "Built $($installer.Name) ($([math]::Round($bytes.Length / 1MB, 1)) MB)"
}
