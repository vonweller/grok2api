[CmdletBinding()]
param()

Set-StrictMode -Version 2.0
$ErrorActionPreference = "Stop"

$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot "..\.."))
$packageScript = [System.IO.File]::ReadAllText((Join-Path $PSScriptRoot "package.ps1"))
$deployScript = [System.IO.File]::ReadAllText((Join-Path $PSScriptRoot "deploy.ps1"))
$pathsSource = [System.IO.File]::ReadAllText((Join-Path $projectRoot "backend\internal\infra\windowsregister\paths.go"))

function Assert-Contains {
    param([string]$Text, [string]$Pattern, [string]$Message)
    if ($Text -notmatch $Pattern) {
        throw $Message
    }
}

function Assert-NotContains {
    param([string]$Text, [string]$Pattern, [string]$Message)
    if ($Text -match $Pattern) {
        throw $Message
    }
}

Assert-Contains $packageScript 'Windows register engine: native Go \(external Chromium\)' "BUILDINFO must identify the native Go registration engine."
Assert-NotContains $packageScript 'Copy-WindowsRegisterEngine|\$registerSource|\$registerDestination' "package.ps1 must not copy the legacy Python registration engine."
Assert-Contains $packageScript 'Forbidden legacy registration engine' "package.ps1 must reject the legacy registration tree from staging."
Assert-Contains $packageScript 'requirements' "package.ps1 must reject Python dependency manifests from staging."

Assert-Contains $deployScript 'ChromeForTestingVersion\s*=\s*"151\.0\.7922\.34"' "deploy.ps1 must pin the approved Chrome for Testing version."
Assert-Contains $deployScript '045621E45A9DD27002C7FC1D8E10FE9F5F71F4CADBF44EC6F397F56F0179725C' "deploy.ps1 must pin the verified Chrome for Testing SHA-256."
Assert-Contains $deployScript 'function Resolve-RegistrationBrowser' "deploy.ps1 must resolve a native Chromium browser."
Assert-Contains $deployScript 'GROK2API_REGISTER_BROWSER' "deploy.ps1 must pass the browser path to the Go process."
Assert-NotContains $deployScript 'Resolve-HostPython|RegisterPythonPath|pip install|CloakBrowser|PYTHONPATH|GROK2API_REGISTER_PYTHON' "deploy.ps1 must not depend on the Python registration runtime."

Assert-NotContains $pathsSource 'resolvePython|resolveLegacyBrowserPath|discoverCloakBrowserRoots|enginePresent' "Go path resolution must not retain legacy Python/CloakBrowser helpers."

$legacyEngine = Join-Path $projectRoot "tools\windows-register"
if ((Test-Path -LiteralPath $legacyEngine) -and
    @(Get-ChildItem -LiteralPath $legacyEngine -Recurse -File -Force -ErrorAction SilentlyContinue).Count -gt 0) {
    throw "Legacy Python registration engine still exists: $legacyEngine"
}

foreach ($scriptPath in @((Join-Path $PSScriptRoot "package.ps1"), (Join-Path $PSScriptRoot "deploy.ps1"))) {
    $tokens = $null
    $errors = $null
    [void][Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$errors)
    if ($errors.Count -gt 0) {
        throw ("PowerShell syntax error in {0}: {1}" -f $scriptPath, (($errors | ForEach-Object { $_.Message }) -join "; "))
    }
}

Write-Host "Native Windows registration packaging checks passed."
