[CmdletBinding()]
param()

Set-StrictMode -Version 2.0
$ErrorActionPreference = "Stop"

$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot "..\.."))
$packageScript = [System.IO.File]::ReadAllText((Join-Path $PSScriptRoot "package.ps1"))
$deployScript = [System.IO.File]::ReadAllText((Join-Path $PSScriptRoot "deploy.ps1"))
$pathsSource = [System.IO.File]::ReadAllText((Join-Path $projectRoot "backend\internal\infra\windowsregister\paths.go"))
$engineRegister = Join-Path $projectRoot "tools\windows-register\grok_register\register.py"

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

Assert-Contains $packageScript 'Copy-WindowsRegisterEngine' "package.ps1 must copy the Python registration engine."
Assert-Contains $packageScript 'tools\windows-register|tools/windows-register|tools\\windows-register' "package.ps1 must stage tools/windows-register."
Assert-Contains $packageScript 'Windows register engine: tools/windows-register \(Python runtime not bundled\)' "BUILDINFO must identify the Python registration engine."
Assert-NotContains $packageScript 'Forbidden legacy registration engine' "package.ps1 must not forbid the Python registration tree."
Assert-Contains $deployScript 'Resolve-HostPython|RegisterPythonPath|pip install|CloakBrowser|GROK2API_REGISTER_PYTHON|Ensure-WindowsRegisterRuntime' "deploy.ps1 must prepare the Python registration runtime."
Assert-Contains $pathsSource 'resolvePython|resolveLegacyBrowserPath|enginePresent' "Go path resolution must retain Python/CloakBrowser helpers."

if (-not (Test-Path -LiteralPath $engineRegister -PathType Leaf)) {
    throw "Python registration engine is missing: $engineRegister"
}

foreach ($scriptPath in @((Join-Path $PSScriptRoot "package.ps1"), (Join-Path $PSScriptRoot "deploy.ps1"))) {
    $tokens = $null
    $errors = $null
    [void][Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$errors)
    if ($errors.Count -gt 0) {
        throw ("PowerShell syntax error in {0}: {1}" -f $scriptPath, (($errors | ForEach-Object { $_.Message }) -join "; "))
    }
}

Write-Host "Python Windows registration packaging checks passed."
