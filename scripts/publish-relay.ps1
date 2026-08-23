param(
  [string]$Remote = "root@192.168.50.217",
  [string]$RemoteDir = "/tmp/codex-link-image-build",
  [string]$Image = "ghcr.io/ayflying/codex_link",
  [string]$GhcrUser = "ayflying",
  [string]$GhcrToken = $env:CODEX_LINK_GHCR_TOKEN,
  [switch]$SkipPush
)

$ErrorActionPreference = "Stop"
$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$versionPath = Join-Path $root "VERSION"
$version = (Get-Content -LiteralPath $versionPath -Raw).Trim()
if ($version -notmatch '^\d+\.\d+\.\d+$') {
  throw "VERSION 不是有效的三段版本号: $version"
}
if ($RemoteDir -notmatch '^/tmp/codex-link-image-build(?:-[A-Za-z0-9_.-]+)?$') {
  throw "RemoteDir 必须是受保护的临时目录: $RemoteDir"
}
$revision = (& git -C $root rev-parse --short HEAD).Trim()
$remoteTarget = "${Remote}:$RemoteDir/"

& ssh $Remote "rm -rf '$RemoteDir' && mkdir -p '$RemoteDir'"
$sources = @(
  (Join-Path $root "apps"),
  (Join-Path $root "cmd"),
  (Join-Path $root "Dockerfile.relay"),
  (Join-Path $root "VERSION"),
  (Join-Path $root "go.mod"),
  (Join-Path $root "go.sum"),
  (Join-Path $root "package.json"),
  (Join-Path $root "package-lock.json")
)
& scp -r @sources $remoteTarget

$hasGhcrToken = -not [string]::IsNullOrWhiteSpace($GhcrToken)
$remoteScript = @"
set -eu
remote_dir='$RemoteDir'
docker_config=''
if [ '$hasGhcrToken' = 'True' ]; then
  docker_config="`$remote_dir/.docker"
  export DOCKER_CONFIG="`$docker_config"
fi
cleanup() {
  if [ -n "`$docker_config" ]; then
    docker logout ghcr.io >/dev/null 2>&1 || true
  fi
  rm -rf "`$remote_dir"
}
trap cleanup EXIT
cd "`$remote_dir"
if [ '$SkipPush' != 'True' ]; then
  timeout --signal=TERM --kill-after=15s 300 \
    docker buildx build \
    --provenance=false \
    --sbom=false \
    --build-arg CODEX_LINK_VERSION='$version' \
    --label org.opencontainers.image.revision='$revision' \
    -t '$Image`:$version' \
    -t '$Image`:latest' \
    --push \
    -f Dockerfile.relay .
  timeout --signal=TERM --kill-after=15s 30 \
    docker buildx imagetools inspect '$Image`:$version' >/dev/null
else
  docker build \
    --provenance=false \
    --sbom=false \
    --build-arg CODEX_LINK_VERSION='$version' \
    --label org.opencontainers.image.revision='$revision' \
    -t '$Image`:$version' \
    -t '$Image`:latest' \
    -f Dockerfile.relay .
  docker image inspect '$Image`:$version' --format 'built image: {{.Id}}'
fi
printf '%s\n' '镜像构建完成: $Image`:$version 和 $Image`:latest'
"@

if ($SkipPush) {
  $remoteScript | & ssh $Remote bash
} elseif ($hasGhcrToken) {
  & ssh $Remote "mkdir -p '$RemoteDir/.docker'"
  $GhcrToken | & ssh $Remote "DOCKER_CONFIG='$RemoteDir/.docker' docker login ghcr.io --username '$GhcrUser' --password-stdin"
  if ($LASTEXITCODE -ne 0) { throw "远程 GHCR 登录失败" }
  $remoteScript | & ssh $Remote bash
} else {
  $remoteScript | & ssh $Remote bash
}
if ($LASTEXITCODE -ne 0) { throw "远程镜像构建或推送失败" }
