param(
  [string]$Remote = $env:CODEX_LINK_BUILD_SERVER,
  [string]$RemoteDir = "/tmp/codex-remote-src"
)

$ErrorActionPreference = "Stop"
if ([string]::IsNullOrWhiteSpace($Remote)) {
  throw "请通过 -Remote 或 CODEX_LINK_BUILD_SERVER 指定远程构建服务器"
}
$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$release = Join-Path $root "release\codex-remote-agent"
$remoteOutput = "$RemoteDir-output"
$version = (Get-Content -LiteralPath (Join-Path $root "VERSION") -Raw).Trim()
if ($version -notmatch '^\d+\.\d+\.\d+$') {
  throw "VERSION 不是有效的三段版本号: $version"
}
$revision = (& git -C $root rev-parse --short HEAD).Trim()
if ($revision -notmatch '^[0-9a-f]+$') {
  throw "无法读取当前 Git 提交版本"
}
$windowsLdflags = "-s -w -H=windowsgui -X main.clientVersion=$version -X main.clientRevision=$revision"

ssh $Remote "rm -rf '$RemoteDir' '$remoteOutput' && mkdir -p '$RemoteDir' '$remoteOutput'"
scp -r "$root\go.mod" "$root\go.sum" "$root\cmd" "$Remote`:$RemoteDir/"
ssh $Remote "cd '$RemoteDir' && go mod download && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='$windowsLdflags' -o '$remoteOutput/codex-remote-agent.exe' ./cmd/codex-remote-agent && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o '$remoteOutput/codex-relay-server' ./cmd/codex-relay-server"

if (Test-Path $release) {
  Remove-Item -LiteralPath $release -Recurse -Force
}
New-Item -ItemType Directory -Path $release | Out-Null
scp "$Remote`:$remoteOutput/codex-remote-agent.exe" "$release\codex-remote-agent.exe"
scp "$Remote`:$remoteOutput/codex-relay-server" "$release\codex-relay-server-linux-amd64"
ssh $Remote "rm -rf '$RemoteDir' '$remoteOutput'"
New-Item -ItemType Directory -Path (Join-Path $release "docs") | Out-Null
Copy-Item -LiteralPath (Join-Path $root "docs\RELAY.md") -Destination (Join-Path $release "docs\RELAY.md") -Force
Copy-Item -LiteralPath (Join-Path $root "docs\API.md") -Destination (Join-Path $release "docs\API.md") -Force

$startAgentCommand = @"
@echo off
setlocal
cd /d "%~dp0"
if exist "codex-remote-agent.next.exe" (
  move /y "codex-remote-agent.next.exe" "codex-remote-agent.exe" >nul
  if errorlevel 1 (
    echo Cannot update the client. Stop all running Codex Link agent processes, then run this script again.
    exit /b 1
  )
)
codex-remote-agent.exe agent
"@
Set-Content -Path (Join-Path $release "start-agent.cmd") -Value $startAgentCommand -Encoding ASCII

Write-Host "Built remote client package: $release"
