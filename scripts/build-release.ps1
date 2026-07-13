param(
    [string]$Version = '',
    [string]$Commit = '',
    [string]$BuildDate = ''
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$output = Join-Path $root 'bin'
New-Item -ItemType Directory -Force -Path $output | Out-Null

$env:GOOS = 'linux'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'

$binary = Join-Path $output 'grok-go-linux-amd64'
Push-Location $root
try {
    if (-not $Version) { $Version = (git describe --tags --always --dirty 2>$null) }
    if (-not $Version) { $Version = 'dev' }
    if (-not $Commit) { $Commit = (git rev-parse HEAD 2>$null) }
    if (-not $Commit) { $Commit = 'unknown' }
    if (-not $BuildDate) { $BuildDate = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ') }
    $package = 'github.com/langrenjh-alt/GROK-GO/internal/buildinfo'
    $ldflags = "-s -w -X $package.Version=$Version -X $package.Commit=$Commit -X $package.Date=$BuildDate"
    go build -trimpath -ldflags $ldflags -o $binary ./cmd/grok-go
    $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $binary).Hash.ToLowerInvariant()
    "$hash  grok-go-linux-amd64" | Set-Content -Encoding ascii (Join-Path $output 'checksums.txt')
} finally {
    Pop-Location
}

Write-Host "Release created at $binary"
