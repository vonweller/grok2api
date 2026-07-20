@echo off
setlocal EnableExtensions DisableDelayedExpansion

set "APP_ROOT=%~dp0."
set "DEPLOY_SCRIPT=%~dp0deploy.ps1"
if not exist "%DEPLOY_SCRIPT%" set "DEPLOY_SCRIPT=%~dp0scripts\windows\deploy.ps1"

if not exist "%DEPLOY_SCRIPT%" (
  echo [ERROR] Missing deployment helper: "%DEPLOY_SCRIPT%"
  exit /b 1
)

if /I "%~1"=="stop" (
  echo [INFO] Stopping Grok2API only. The startup task will remain installed.
  powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%DEPLOY_SCRIPT%" -AppRoot "%APP_ROOT%" stop
) else if /I "%~1"=="status" (
  powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%DEPLOY_SCRIPT%" -AppRoot "%APP_ROOT%" status
) else if "%~1"=="" (
  echo [INFO] Stopping Grok2API and removing its startup task.
  echo [INFO] Config and data will be preserved. Run deploy.bat install to enable startup again.
  powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%DEPLOY_SCRIPT%" -AppRoot "%APP_ROOT%" uninstall
) else (
  echo Usage:
  echo   kill.bat          Stop process and disable startup task
  echo   kill.bat stop     Stop process but keep startup task
  echo   kill.bat status   Show process and startup status
  exit /b 2
)
set "EXIT_CODE=%ERRORLEVEL%"

echo.
if "%EXIT_CODE%"=="0" (
  echo [OK] Kill command completed.
) else (
  echo [ERROR] Kill command failed with exit code %EXIT_CODE%.
)
if "%~1"=="" if not defined GROK2API_NO_PAUSE pause
exit /b %EXIT_CODE%
