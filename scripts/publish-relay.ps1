param(
  [string]$Remote = $env:CODEX_LINK_BUILD_SERVER,
  [string]$RemoteDir = "/tmp/codex-link-image-build",
  [string]$Image = "ghcr.io/ayflying/codex_link",
  [string]$GhcrUser = "ayflying",
  [string]$GhcrToken = $env:CODEX_LINK_GHCR_TOKEN,
  [switch]$SkipPush
)

$ErrorActionPreference = "Stop"
$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($Remote)) {
  $Remote = [string](& git -C $root config --get codex-link.build-server 2>$null).Trim()
}
if ([string]::IsNullOrWhiteSpace($Remote)) {
  throw "请通过 -Remote、CODEX_LINK_BUILD_SERVER 或本地 Git 配置 codex-link.build-server 指定远程构建服务器"
}
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
$archivePath = Join-Path ([System.IO.Path]::GetTempPath()) ("codex-link-image-build-$revision-$([guid]::NewGuid().ToString('N')).zip")

try {
  # 只发布 HEAD 快照，保证镜像内容与标签对应的提交完全一致。
  & git -C $root archive --format=zip --output=$archivePath HEAD
  if ($LASTEXITCODE -ne 0) { throw "无法创建 Git 发布快照" }
  & ssh $Remote "rm -rf '$RemoteDir' && mkdir -p '$RemoteDir'"
  if ($LASTEXITCODE -ne 0) { throw "无法创建远程构建目录" }
  & scp $archivePath "${remoteTarget}source.zip"
  if ($LASTEXITCODE -ne 0) { throw "无法上传 Git 发布快照" }
  & ssh $Remote "cd '$RemoteDir' && unzip -q source.zip && rm -f source.zip"
  if ($LASTEXITCODE -ne 0) { throw "无法解压远程 Git 发布快照" }

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
} finally {
  Remove-Item -LiteralPath $archivePath -Force -ErrorAction SilentlyContinue
}
