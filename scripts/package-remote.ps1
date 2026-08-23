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

ssh $Remote "rm -rf '$RemoteDir' '$remoteOutput' && mkdir -p '$RemoteDir' '$remoteOutput'"
scp -r "$root\go.mod" "$root\go.sum" "$root\cmd" "$Remote`:$RemoteDir/"
ssh $Remote "cd '$RemoteDir' && go mod download && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w -H=windowsgui' -o '$remoteOutput/codex-remote-agent.exe' ./cmd/codex-remote-agent && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o '$remoteOutput/codex-relay-server' ./cmd/codex-relay-server"

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

@"
@echo off
setlocal
cd /d "%~dp0"
codex-remote-agent.exe agent
"@ | Set-Content -Path (Join-Path $release "start-agent.cmd") -Encoding ASCII

Write-Host "Built remote client package: $release"
