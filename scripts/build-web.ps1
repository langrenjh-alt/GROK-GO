$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$source = Join-Path $root 'web\out'
$target = Join-Path $root 'internal\webui\dist'

if (-not (Test-Path -LiteralPath $source)) {
    throw "Frontend output was not found at $source. Run pnpm --dir web build first."
}

New-Item -ItemType Directory -Force -Path $target | Out-Null
Get-ChildItem -LiteralPath $target -Force | Where-Object Name -ne '.gitkeep' | Remove-Item -Recurse -Force
Copy-Item -Path (Join-Path $source '*') -Destination $target -Recurse -Force

if (-not (Test-Path -LiteralPath (Join-Path $target 'index.html'))) {
    throw 'Frontend export does not contain index.html.'
}

Write-Host "Embedded frontend staged at $target"
