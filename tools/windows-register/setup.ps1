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
# 保证从任意工作目录执行 setup.ps1 时都能 import grok_register。
$oldPythonPath = $env:PYTHONPATH
$oldPythonUtf8 = $env:PYTHONUTF8
try {
    $env:PYTHONPATH = $PSScriptRoot
    $env:PYTHONUTF8 = "1"
    $browserPath = & $venvPython -c "from grok_register.register import find_chrome; print(find_chrome())" 2>&1
}
finally {
    if ($null -eq $oldPythonPath) { Remove-Item Env:PYTHONPATH -ErrorAction SilentlyContinue } else { $env:PYTHONPATH = $oldPythonPath }
    if ($null -eq $oldPythonUtf8) { Remove-Item Env:PYTHONUTF8 -ErrorAction SilentlyContinue } else { $env:PYTHONUTF8 = $oldPythonUtf8 }
}
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace([string]$browserPath)) {
    $detail = (($browserPath | ForEach-Object { "$_" }) -join [Environment]::NewLine).Trim()
    if ([string]::IsNullOrWhiteSpace($detail)) {
        throw "CloakBrowser was not found. Run setup.ps1 without -SkipBrowserInstall."
    }
    throw ("CloakBrowser was not found. Run setup.ps1 without -SkipBrowserInstall. {0}" -f $detail)
}
$browserPath = ([string]$browserPath).Trim()
if ($browserPath -match "[\r\n]") {
    $browserPath = (@($browserPath -split "[\r\n]+" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }) | Select-Object -Last 1).Trim()
}
Write-Host $browserPath
# Persist for service accounts (e.g. LOCAL SERVICE) that cannot see the interactive profile.
$markerPath = Join-Path $PSScriptRoot ".browser-path"
[System.IO.File]::WriteAllText($markerPath, $browserPath + [Environment]::NewLine, [Text.Encoding]::UTF8)
$env:CLOAKBROWSER_EXECUTABLE_PATH = $browserPath

if ($SmokeTest) {
    $smoke = Join-Path $PSScriptRoot "scripts\windows_browser_smoke.py"
    if (Test-Path -LiteralPath $smoke) {
        & $venvPython $smoke
        if ($LASTEXITCODE -ne 0) { throw "Browser smoke test failed." }
    } else {
        Write-Host "Smoke script missing; skipped browser smoke test."
    }
}

Write-Host "Windows setup completed."
Write-Host "Browser path saved to .browser-path for managed service startup."
