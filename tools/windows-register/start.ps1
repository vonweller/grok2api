[CmdletBinding()]
param(
    [switch]$EmailService,
    [switch]$AuthService,
    [switch]$Reconfigure,
    [switch]$VerboseLog,
    [int]$Target = -1,
    [string]$MaxMem = "",
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$ExtraArgs
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
Set-Location $PSScriptRoot

function Write-Utf8NoBom([string]$Path, [string]$Content) {
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($Path, $Content, $encoding)
}

function Import-DotEnv([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return }
    foreach ($rawLine in [System.IO.File]::ReadAllLines($Path)) {
        $line = $rawLine.Trim()
        if (-not $line -or $line.StartsWith("#")) { continue }
        $separator = $line.IndexOf("=")
        if ($separator -lt 1) { continue }
        $name = $line.Substring(0, $separator).Trim()
        $value = $line.Substring($separator + 1).Trim()
        if ($value.Length -ge 2) {
            if (($value.StartsWith('"') -and $value.EndsWith('"')) -or
                ($value.StartsWith("'") -and $value.EndsWith("'"))) {
                $value = $value.Substring(1, $value.Length - 2)
            }
        }
        [Environment]::SetEnvironmentVariable($name, $value, "Process")
    }
}

$venvPython = Join-Path $PSScriptRoot ".venv\Scripts\python.exe"
if (-not (Test-Path -LiteralPath $venvPython)) {
    Write-Host "First run: installing the Windows runtime..."
    & (Join-Path $PSScriptRoot "setup.ps1")
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}

$envPath = Join-Path $PSScriptRoot ".env"
if ($Reconfigure -or -not (Test-Path -LiteralPath $envPath)) {
    Write-Host ""
    Write-Host "Email mode:"
    Write-Host "  [1] Public temporary email (default)"
    Write-Host "  [2] Custom domain email webhook"
    $mode = Read-Host "Choose 1 or 2 [1]"
    if ($mode -eq "2") {
        $domain = Read-Host "Email domain"
        if ([string]::IsNullOrWhiteSpace($domain)) {
            throw "Email domain is required for custom mode."
        }
        $api = Read-Host "Local email API [http://127.0.0.1:8080]"
        if ([string]::IsNullOrWhiteSpace($api)) { $api = "http://127.0.0.1:8080" }
        Write-Utf8NoBom $envPath "EMAIL_MODE=custom`nEMAIL_DOMAIN=$domain`nEMAIL_API=$api`n"
    } else {
        Write-Utf8NoBom $envPath "EMAIL_MODE=tempmail`n"
    }
    Write-Host "Created .env."
}

Import-DotEnv $envPath

$module = "grok_register.register"
$lockName = "register"
$moduleArgs = @()
if ($EmailService) {
    $module = "grok_register.email_server"
    $lockName = "email-service"
} elseif ($AuthService) {
    $module = "xai_enroller.service"
    $lockName = "auth-service"
} else {
    if ($VerboseLog) { $moduleArgs += "--debug" }
    if ($Target -ge 0) { $moduleArgs += @("--target", [string]$Target) }
    if (-not [string]::IsNullOrWhiteSpace($MaxMem)) {
        $moduleArgs += @("--max-mem", $MaxMem)
    }
}
if ($ExtraArgs) { $moduleArgs += $ExtraArgs }

New-Item -ItemType Directory -Force -Path "logs" | Out-Null
$lockPath = Join-Path $PSScriptRoot "logs\$lockName.lock"
try {
    $lockStream = [System.IO.File]::Open(
        $lockPath,
        [System.IO.FileMode]::OpenOrCreate,
        [System.IO.FileAccess]::ReadWrite,
        [System.IO.FileShare]::None
    )
} catch {
    throw "$lockName is already running in this project directory."
}

try {
    Write-Host "Starting $module (Ctrl-C to stop)..."
    & $venvPython -m $module @moduleArgs
    $exitCode = $LASTEXITCODE
} finally {
    $lockStream.Dispose()
}
exit $exitCode
