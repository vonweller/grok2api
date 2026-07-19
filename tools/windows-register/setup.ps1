[CmdletBinding()]
param(
    [switch]$SkipBrowserInstall,
    [switch]$SmokeTest
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
Set-Location $PSScriptRoot

$pythonCommand = Get-Command python -ErrorAction SilentlyContinue
if (-not $pythonCommand) {
    throw "Python 3.10+ was not found in PATH."
}

$venvPython = Join-Path $PSScriptRoot ".venv\Scripts\python.exe"
if (-not (Test-Path -LiteralPath $venvPython)) {
    Write-Host "[1/4] Creating the Windows virtual environment..."
    & $pythonCommand.Source -m venv .venv
    if ($LASTEXITCODE -ne 0) { throw "Failed to create .venv." }
}

Write-Host "[2/4] Installing Python dependencies..."
& $venvPython -m pip install --upgrade pip setuptools wheel
if ($LASTEXITCODE -ne 0) { throw "Failed to upgrade pip tooling." }
& $venvPython -m pip install -r requirements.txt
if ($LASTEXITCODE -ne 0) { throw "Failed to install requirements.txt." }

if (-not $SkipBrowserInstall) {
    Write-Host "[3/4] Installing CloakBrowser Chromium..."
    & $venvPython -m cloakbrowser install
    if ($LASTEXITCODE -ne 0) { throw "CloakBrowser Chromium installation failed." }
} else {
    Write-Host "[3/4] Browser installation skipped."
}

New-Item -ItemType Directory -Force -Path "keys", "logs" | Out-Null

Write-Host "[4/4] Checking the runtime..."
& $venvPython -c "from grok_register.register import find_chrome; print(find_chrome())"
if ($LASTEXITCODE -ne 0) {
    throw "CloakBrowser was not found. Run setup.ps1 without -SkipBrowserInstall."
}

if ($SmokeTest) {
    & $venvPython scripts\windows_browser_smoke.py
    if ($LASTEXITCODE -ne 0) { throw "Browser smoke test failed." }
}

Write-Host "Windows setup completed."
