@echo off
setlocal EnableExtensions DisableDelayedExpansion

set "APP_ROOT=%~dp0."
set "DEPLOY_SCRIPT=%~dp0deploy.ps1"
set "EXIT_CODE=0"
if not exist "%DEPLOY_SCRIPT%" set "DEPLOY_SCRIPT=%~dp0scripts\windows\deploy.ps1"

if not exist "%DEPLOY_SCRIPT%" (
  echo [ERROR] Missing deployment helper: "%DEPLOY_SCRIPT%"
  set "EXIT_CODE=1"
  goto :finish
)

if /I not "%~1"=="help" if not exist "%APP_ROOT%\grok2api.exe" if exist "%~dp0package.bat" (
  echo [ERROR] This deploy.bat is in the source repository, not in a release package.
  echo [INFO] Run package.bat first, extract the matching ZIP from the release folder,
  echo [INFO] then run deploy.bat inside that extracted directory.
  set "EXIT_CODE=1"
  goto :finish
)

powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%DEPLOY_SCRIPT%" -AppRoot "%APP_ROOT%" %*
set "EXIT_CODE=%ERRORLEVEL%"

:finish
if "%EXIT_CODE%"=="0" (
  echo.
  echo [OK] Grok2API deployment command completed.
) else (
  echo.
  echo [ERROR] Grok2API deployment command failed with exit code %EXIT_CODE%.
)

if "%~1"=="" if not defined GROK2API_NO_PAUSE (
  echo.
  echo Press any key to close this window...
  pause >nul
)

exit /b %EXIT_CODE%
