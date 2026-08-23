$ErrorActionPreference = "Stop"

$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
git -C $root config core.hooksPath .githooks
Write-Host "已启用 Git hooks: .githooks"
