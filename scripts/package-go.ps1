$ErrorActionPreference = "Stop"

$root = Resolve-Path (Join-Path $PSScriptRoot "..")
$release = Join-Path $root "release\codex-go-remote"
$web = Join-Path $release "web"

Set-Location $root
npm --workspace apps/web run build

if (Test-Path $release) {
  Remove-Item -LiteralPath $release -Recurse -Force
}
New-Item -ItemType Directory -Path $web | Out-Null

go build -ldflags "-s -w" -o (Join-Path $release "codex-go-remote.exe") .\cmd\codex-local-server
Copy-Item -Path (Join-Path $root "apps\web\dist\*") -Destination $web -Recurse -Force
if (Test-Path (Join-Path $root "docs")) {
  Copy-Item -Path (Join-Path $root "docs") -Destination (Join-Path $release "docs") -Recurse -Force
}

@"
@echo off
setlocal
cd /d "%~dp0"
codex-go-remote.exe
"@ | Set-Content -Path (Join-Path $release "start.cmd") -Encoding ASCII

Write-Host "Built portable package: $release"
