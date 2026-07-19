@echo off
setlocal EnableExtensions DisableDelayedExpansion

set "PROJECT_ROOT=%~dp0"
set "PACKAGE_SCRIPT=%PROJECT_ROOT%scripts\windows\package.ps1"
set "EXIT_CODE=0"

if not exist "%PACKAGE_SCRIPT%" (
  echo [ERROR] Missing packaging helper: "%PACKAGE_SCRIPT%"
  set "EXIT_CODE=1"
  goto :finish
)

powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%PACKAGE_SCRIPT%" %*
set "EXIT_CODE=%ERRORLEVEL%"

:finish
if "%EXIT_CODE%"=="0" (
  echo.
  echo [OK] Grok2API packaging command completed.
) else (
  echo.
  echo [ERROR] Grok2API packaging command failed with exit code %EXIT_CODE%.
)

if "%~1"=="" if not defined GROK2API_NO_PAUSE (
  echo.
  echo Press any key to close this window...
  pause >nul
)

exit /b %EXIT_CODE%
