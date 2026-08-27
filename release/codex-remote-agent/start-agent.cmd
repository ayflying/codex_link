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
