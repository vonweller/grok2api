[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet("install", "start", "stop", "restart", "status", "logs", "run", "run-task", "uninstall", "help")]
    [string]$Action = "install",

    [Parameter(Position = 1)]
    [ValidateRange(0, 65535)]
    [int]$Port = 0,

    [string]$AppRoot = ""
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = "Stop"

$ScriptFile = [System.IO.Path]::GetFullPath($MyInvocation.MyCommand.Path)
if ([string]::IsNullOrWhiteSpace($AppRoot)) {
    $AppRoot = $PSScriptRoot
}
$Root = [System.IO.Path]::GetFullPath($AppRoot)
$ExecutablePath = Join-Path $Root "grok2api.exe"
$ConfigPath = Join-Path $Root "config.yaml"
$ConfigTemplatePath = Join-Path $Root "config.example.yaml"
$FrontendIndexPath = Join-Path $Root "frontend\dist\index.html"
$PlatformPath = Join-Path $Root "PACKAGE_PLATFORM"
$PackageChecksumsPath = Join-Path $Root "SHA256SUMS.txt"
$DataPath = Join-Path $Root "data"
$LogsPath = Join-Path $Root "logs"
$PidPath = Join-Path $DataPath "grok2api.pid"
$PortPath = Join-Path $DataPath "grok2api.port"
$StdoutPath = Join-Path $LogsPath "grok2api.out.log"
$StderrPath = Join-Path $LogsPath "grok2api.err.log"
$TaskLogPath = Join-Path $LogsPath "grok2api-task.log"
$CredentialsPath = Join-Path $Root "FIRST_RUN_CREDENTIALS.txt"
$RegisterEnginePath = Join-Path $Root "tools\windows-register"
$RegisterOutputPath = Join-Path $DataPath "windows-register"
$RegisterPythonPath = Join-Path $RegisterEnginePath ".venv\Scripts\python.exe"
$RegisterBrowserMarkerPath = Join-Path $RegisterEnginePath ".browser-path"
$normalizedTaskRoot = $Root.TrimEnd("\").ToLowerInvariant()
$taskHashProvider = [Security.Cryptography.SHA256]::Create()
try {
    $taskHashBytes = $taskHashProvider.ComputeHash([Text.Encoding]::UTF8.GetBytes($normalizedTaskRoot))
}
finally {
    $taskHashProvider.Dispose()
}
$taskHash = -join ($taskHashBytes[0..5] | ForEach-Object { $_.ToString("x2") })
$TaskName = "Grok2API-$taskHash"

function Write-Step {
    param([string]$Message)
    Write-Host ("[Grok2API] " + $Message) -ForegroundColor Cyan
}

function Write-WarningLine {
    param([string]$Message)
    Write-Host ("[WARNING] " + $Message) -ForegroundColor Yellow
}

function Assert-NoReparsePathChain {
    param([string]$Path, [string]$Boundary)
    $full = [System.IO.Path]::GetFullPath($Path)
    $boundaryFull = [System.IO.Path]::GetFullPath($Boundary).TrimEnd("\")
    if (-not ($full.Equals($boundaryFull, [StringComparison]::OrdinalIgnoreCase) -or
        $full.StartsWith($boundaryFull + "\", [StringComparison]::OrdinalIgnoreCase))) {
        throw "Path is outside its deployment safety boundary: $full"
    }
    $current = $full
    while ($current.Length -ge $boundaryFull.Length) {
        if (Test-Path -LiteralPath $current) {
            $item = Get-Item -LiteralPath $current -Force
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Deployment paths cannot contain a junction or symbolic link: $current"
            }
        }
        if ($current.Equals($boundaryFull, [StringComparison]::OrdinalIgnoreCase)) {
            break
        }
        $parent = Split-Path -Parent $current
        if ([string]::IsNullOrWhiteSpace($parent) -or $parent -eq $current) {
            break
        }
        $current = $parent
    }
}

function Assert-SafeAppRoot {
    $driveRoot = [System.IO.Path]::GetPathRoot($Root)
    if ($Root.TrimEnd("\").Equals($driveRoot.TrimEnd("\"), [StringComparison]::OrdinalIgnoreCase)) {
        throw "Do not deploy directly in a drive root. Use a dedicated folder such as D:\Services\grok2api."
    }
    Assert-NoReparsePathChain $Root $driveRoot
}

function Assert-LocalNTFSVolume {
    $driveRoot = [System.IO.Path]::GetPathRoot($Root)
    $drive = New-Object IO.DriveInfo($driveRoot)
    if (-not $drive.IsReady -or $drive.DriveType -ne [IO.DriveType]::Fixed -or $drive.DriveFormat -ne "NTFS") {
        throw "Deploy to a fixed local NTFS volume. Detected type '$($drive.DriveType)' and format '$($drive.DriveFormat)'."
    }
    $substPrefix = $drive.Name.TrimEnd("\") + "\: =>"
    foreach ($line in @(& "$env:SystemRoot\System32\subst.exe" 2>$null)) {
        if ($line.TrimStart().StartsWith($substPrefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "SUBST drives are not supported by the startup task. Deploy to the underlying fixed NTFS path."
        }
    }
    foreach ($path in @(
        $ExecutablePath,
        $ConfigPath,
        $ConfigTemplatePath,
        $PlatformPath,
        $PackageChecksumsPath,
        (Join-Path $Root "frontend\dist"),
        $DataPath,
        (Join-Path $DataPath "media"),
        $LogsPath
    )) {
        Assert-NoReparsePathChain $path $Root
    }
}

function Test-IsAdministrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Invoke-Elevated {
    $argumentLine = '-NoLogo -NoProfile -ExecutionPolicy Bypass -File "{0}" -AppRoot "{1}" -Action {2} -Port {3}' -f $ScriptFile, $Root, $Action, $Port
    Write-Step "Administrator permission is required. Requesting elevation..."
    $elevated = Start-Process -FilePath "powershell.exe" -Verb RunAs -ArgumentList $argumentLine -Wait -PassThru
    exit $elevated.ExitCode
}

function Get-NativeArchitecture {
    $value = $env:PROCESSOR_ARCHITEW6432
    if ([string]::IsNullOrWhiteSpace($value)) {
        $value = $env:PROCESSOR_ARCHITECTURE
    }
    switch ($value.ToUpperInvariant()) {
        "AMD64" { return "amd64" }
        "ARM64" { return "arm64" }
        default { return $value.ToLowerInvariant() }
    }
}

function Assert-DeploymentEnvironment {
    if ($PSVersionTable.PSVersion -lt [version]"5.1") {
        throw "Windows PowerShell 5.1 or later is required."
    }
    if ($Root.StartsWith("\\")) {
        throw "Deploy from a local NTFS path, not a UNC/network path."
    }
    Assert-LocalNTFSVolume
    foreach ($requiredFile in @($ExecutablePath, $ConfigTemplatePath, $FrontendIndexPath, $PackageChecksumsPath)) {
        Assert-NoReparsePathChain $requiredFile $Root
        if (-not (Test-Path -LiteralPath $requiredFile -PathType Leaf)) {
            throw "Incomplete release package. Missing: $requiredFile"
        }
    }
    if (Test-Path -LiteralPath $PlatformPath -PathType Leaf) {
        $expected = ([System.IO.File]::ReadAllText($PlatformPath)).Trim()
        $actual = "windows/$(Get-NativeArchitecture)"
        if ($expected -ne $actual) {
            throw "Package platform is $expected, but this server is $actual."
        }
    }
    Assert-PackageIntegrity
    [System.IO.Directory]::CreateDirectory($DataPath) | Out-Null
    [System.IO.Directory]::CreateDirectory($LogsPath) | Out-Null
    Assert-NoReparsePathChain $DataPath $Root
    Assert-NoReparsePathChain $LogsPath $Root
    Write-Step "Environment OK: self-contained executable; Go, Node.js, and pnpm are not required on this server."
}

function Get-FileSha256 {
    param([string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Assert-PackageIntegrity {
    $lines = [System.IO.File]::ReadAllLines($PackageChecksumsPath)
    if ($lines.Count -eq 0) {
        throw "Package checksum manifest is empty."
    }
    $rootPrefix = $Root.TrimEnd("\") + "\"
    $seen = @{}
    foreach ($line in $lines) {
        $match = [regex]::Match($line, "^([0-9a-fA-F]{64})  (.+)$")
        if (-not $match.Success) {
            throw "Invalid package checksum entry: $line"
        }
        $relative = $match.Groups[2].Value.Replace("/", "\")
        if ([System.IO.Path]::IsPathRooted($relative)) {
            throw "Package checksum path must be relative: $relative"
        }
        $fullPath = [System.IO.Path]::GetFullPath((Join-Path $Root $relative))
        if (-not $fullPath.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Package checksum path escapes the deployment directory: $relative"
        }
        Assert-NoReparsePathChain $fullPath $Root
        if ($seen.ContainsKey($fullPath)) {
            throw "Duplicate package checksum path: $relative"
        }
        $seen[$fullPath] = $true
        if (-not (Test-Path -LiteralPath $fullPath -PathType Leaf)) {
            throw "Package file listed in SHA256SUMS.txt is missing: $relative"
        }
        $manifestItem = Get-Item -LiteralPath $fullPath -Force
        if (($manifestItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Package files cannot be symbolic links: $relative"
        }
        if ((Get-FileSha256 $fullPath) -ne $match.Groups[1].Value.ToLowerInvariant()) {
            throw "Package file checksum mismatch: $relative"
        }
    }
    Write-Step "Package integrity verified ($($seen.Count) files)."
}

function New-RandomBytes {
    param([int]$Count)
    [byte[]]$bytes = New-Object byte[] $Count
    $generator = [Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $generator.GetBytes($bytes)
    }
    finally {
        $generator.Dispose()
    }
    return ,$bytes
}

function New-HexSecret {
    $bytes = New-RandomBytes 32
    return -join ($bytes | ForEach-Object { $_.ToString("x2") })
}

function New-Base64Secret {
    return [Convert]::ToBase64String((New-RandomBytes 32))
}

function New-AdminPassword {
    return [Convert]::ToBase64String((New-RandomBytes 24)).TrimEnd("=").Replace("+", "-").Replace("/", "_")
}

function Protect-SensitiveFile {
    param([string]$Path, [switch]$AllowLocalServiceRead)
    $acl = New-Object Security.AccessControl.FileSecurity
    $acl.SetAccessRuleProtection($true, $false)
    $sidValues = @(
        [Security.Principal.WindowsIdentity]::GetCurrent().User.Value,
        "S-1-5-18",
        "S-1-5-32-544"
    ) | Select-Object -Unique
    foreach ($sidValue in $sidValues) {
        $sid = New-Object Security.Principal.SecurityIdentifier($sidValue)
        $rule = New-Object Security.AccessControl.FileSystemAccessRule(
            $sid,
            [Security.AccessControl.FileSystemRights]::FullControl,
            [Security.AccessControl.AccessControlType]::Allow
        )
        $acl.AddAccessRule($rule) | Out-Null
    }
    if ($AllowLocalServiceRead) {
        $localServiceSid = New-Object Security.Principal.SecurityIdentifier("S-1-5-19")
        $readRule = New-Object Security.AccessControl.FileSystemAccessRule(
            $localServiceSid,
            [Security.AccessControl.FileSystemRights]::Read,
            [Security.AccessControl.AccessControlType]::Allow
        )
        $acl.AddAccessRule($readRule) | Out-Null
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}

function Set-DeploymentDirectoryAcl {
    param(
        [string]$Path,
        [Security.AccessControl.FileSystemRights]$LocalServiceRights
    )
    $acl = New-Object Security.AccessControl.DirectorySecurity
    $acl.SetAccessRuleProtection($true, $false)
    $inheritance = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
    $sidValues = @(
        [Security.Principal.WindowsIdentity]::GetCurrent().User.Value,
        "S-1-5-18",
        "S-1-5-32-544"
    ) | Select-Object -Unique
    foreach ($sidValue in $sidValues) {
        $sid = New-Object Security.Principal.SecurityIdentifier($sidValue)
        $rule = New-Object Security.AccessControl.FileSystemAccessRule(
            $sid,
            [Security.AccessControl.FileSystemRights]::FullControl,
            $inheritance,
            [Security.AccessControl.PropagationFlags]::None,
            [Security.AccessControl.AccessControlType]::Allow
        )
        $acl.AddAccessRule($rule) | Out-Null
    }
    $localServiceSid = New-Object Security.Principal.SecurityIdentifier("S-1-5-19")
    $localServiceRule = New-Object Security.AccessControl.FileSystemAccessRule(
        $localServiceSid,
        $LocalServiceRights,
        $inheritance,
        [Security.AccessControl.PropagationFlags]::None,
        [Security.AccessControl.AccessControlType]::Allow
    )
    $acl.AddAccessRule($localServiceRule) | Out-Null
    Set-Acl -LiteralPath $Path -AclObject $acl
}

function Protect-DeploymentDirectory {
    Assert-SafeAppRoot
    Assert-LocalNTFSVolume
    Set-DeploymentDirectoryAcl $DataPath ([Security.AccessControl.FileSystemRights]::Modify)
    Set-DeploymentDirectoryAcl $LogsPath ([Security.AccessControl.FileSystemRights]::Modify)
    Set-DeploymentDirectoryAcl $Root ([Security.AccessControl.FileSystemRights]::ReadAndExecute)
    if (Test-Path -LiteralPath $RegisterOutputPath -PathType Container) {
        Set-DeploymentDirectoryAcl $RegisterOutputPath ([Security.AccessControl.FileSystemRights]::Modify)
    }
    Protect-SensitiveFile $ConfigPath -AllowLocalServiceRead
    if (Test-Path -LiteralPath $CredentialsPath -PathType Leaf) {
        Protect-SensitiveFile $CredentialsPath
    }
    Write-Step "Applied least-privilege ACLs: application files are read-only to LOCAL SERVICE; only data and logs are writable."
}

function Test-IsAdministratorAccount {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Resolve-HostPython {
    # Prefer the Windows Python launcher with an explicit major version so we do not
    # pick the Microsoft Store stub or a broken "python" shim without a real install.
    $pyLauncher = Get-Command py -ErrorAction SilentlyContinue
    if ($null -ne $pyLauncher) {
        try {
            $launched = & $pyLauncher.Source -3 -c "import sys; print(sys.executable)" 2>$null
            if ($LASTEXITCODE -eq 0) {
                $candidate = ([string]$launched).Trim()
                if (-not [string]::IsNullOrWhiteSpace($candidate) -and
                    (Test-Path -LiteralPath $candidate -PathType Leaf) -and
                    ($candidate -notmatch "(?i)\\WindowsApps\\")) {
                    return $candidate
                }
            }
        }
        catch {
            # Fall through to PATH and common install locations.
        }
    }

    $commands = @("python", "python3", "py")
    foreach ($name in $commands) {
        $command = Get-Command $name -ErrorAction SilentlyContinue
        if ($null -eq $command) {
            continue
        }
        $source = [string]$command.Source
        if ([string]::IsNullOrWhiteSpace($source)) {
            continue
        }
        # Prefer real installs over WindowsApps stubs.
        if ($source -match "(?i)\\WindowsApps\\") {
            continue
        }
        return $source
    }
    $globs = @(
        "C:\Python3*\python.exe",
        "C:\Program Files\Python3*\python.exe",
        "C:\Program Files\Python*\python.exe",
        "$env:LOCALAPPDATA\Programs\Python\Python3*\python.exe",
        "G:\Python\python.exe"
    )
    foreach ($pattern in $globs) {
        $hit = Get-Item -Path $pattern -ErrorAction SilentlyContinue | Sort-Object FullName -Descending | Select-Object -First 1
        if ($null -ne $hit -and (Test-Path -LiteralPath $hit.FullName -PathType Leaf)) {
            return $hit.FullName
        }
    }
    return ""
}

# Returns true when tools\windows-register can create a local .venv and install packages.
function Test-RegisterEngineWritable {
    try {
        [System.IO.Directory]::CreateDirectory($RegisterEnginePath) | Out-Null
        $probe = Join-Path $RegisterEnginePath (".write-probe-" + [Guid]::NewGuid().ToString("N") + ".tmp")
        [System.IO.File]::WriteAllText($probe, "ok")
        Remove-Item -LiteralPath $probe -Force -ErrorAction SilentlyContinue
        return $true
    }
    catch {
        return $false
    }
}

function Get-RegisterBrowserPath {
    if (Test-Path -LiteralPath $RegisterBrowserMarkerPath -PathType Leaf) {
        $marked = ([System.IO.File]::ReadAllText($RegisterBrowserMarkerPath)).Trim().Trim('"')
        if (-not [string]::IsNullOrWhiteSpace($marked) -and (Test-Path -LiteralPath $marked -PathType Leaf)) {
            return $marked
        }
    }
    $searchRoots = @(
        (Join-Path $RegisterEnginePath ".cloakbrowser"),
        (Join-Path $RegisterEnginePath "browser"),
        (Join-Path $RegisterEnginePath "AppData\Local\cloakbrowser"),
        (Join-Path $env:USERPROFILE ".cloakbrowser")
    )
    if (-not [string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
        $searchRoots += (Join-Path $env:LOCALAPPDATA "cloakbrowser")
    }
    $newest = $null
    $newestTime = [DateTime]::MinValue
    foreach ($root in $searchRoots) {
        if ([string]::IsNullOrWhiteSpace($root) -or -not (Test-Path -LiteralPath $root)) {
            continue
        }
        Get-ChildItem -LiteralPath $root -Recurse -Filter "chrome.exe" -ErrorAction SilentlyContinue | ForEach-Object {
            if ($_.LastWriteTimeUtc -gt $newestTime) {
                $newest = $_.FullName
                $newestTime = $_.LastWriteTimeUtc
            }
        }
    }
    if ($null -ne $newest) {
        return $newest
    }
    return ""
}

# Import names for requirements.txt packages (python-dotenv imports as dotenv).
function Get-RegisterRequiredPythonModules {
    return @("cloakbrowser", "requests", "dotenv", "httpx", "playwright", "curl_cffi")
}

# Returns missing import names for the package-local virtualenv.
function Get-MissingRegisterPythonModules {
    if (-not (Test-Path -LiteralPath $RegisterPythonPath -PathType Leaf)) {
        return ,(Get-RegisterRequiredPythonModules)
    }
    $modules = Get-RegisterRequiredPythonModules
    $moduleList = ($modules | ForEach-Object { "'" + $_ + "'" }) -join ","
    $probe = @"
import importlib.util
mods = [$moduleList]
missing = [m for m in mods if importlib.util.find_spec(m) is None]
print(','.join(missing))
"@
    try {
        $output = & $RegisterPythonPath -c $probe 2>$null
        if ($LASTEXITCODE -ne 0) {
            return ,$modules
        }
        $text = ([string]$output).Trim()
        if ([string]::IsNullOrWhiteSpace($text)) {
            return @()
        }
        return @($text -split "," | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    }
    catch {
        return ,$modules
    }
}

function Test-RegisterPythonPackagesReady {
    $missing = @(Get-MissingRegisterPythonModules)
    return ($missing.Count -eq 0)
}

function Write-RegisterBrowserMarker {
    param([string]$BrowserPath)
    if ([string]::IsNullOrWhiteSpace($BrowserPath)) {
        return
    }
    try {
        [System.IO.File]::WriteAllText($RegisterBrowserMarkerPath, $BrowserPath + [Environment]::NewLine, [Text.Encoding]::UTF8)
    }
    catch {
        Write-WarningLine ("Could not write .browser-path ({0}). Continuing with discovered browser path." -f $_.Exception.Message)
    }
    $env:CLOAKBROWSER_EXECUTABLE_PATH = $BrowserPath
}

function Test-WindowsRegisterRuntimeReady {
    if (-not (Test-Path -LiteralPath (Join-Path $RegisterEnginePath "grok_register\register.py") -PathType Leaf)) {
        return $false
    }
    if (-not (Test-Path -LiteralPath $RegisterPythonPath -PathType Leaf)) {
        return $false
    }
    if (-not (Test-RegisterPythonPackagesReady)) {
        return $false
    }
    $browser = Get-RegisterBrowserPath
    if ([string]::IsNullOrWhiteSpace($browser)) {
        return $false
    }
    return $true
}

function Install-RegisterPythonPackages {
    param([string]$HostPython)

    $venvDir = Join-Path $RegisterEnginePath ".venv"
    if (-not (Test-Path -LiteralPath $RegisterPythonPath -PathType Leaf)) {
        Write-Step "Creating register virtualenv..."
        & $HostPython -m venv $venvDir
        if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $RegisterPythonPath -PathType Leaf)) {
            throw "Failed to create tools\windows-register\.venv. Check the Python installation."
        }
    }

    $requirements = Join-Path $RegisterEnginePath "requirements.txt"
    if (-not (Test-Path -LiteralPath $requirements -PathType Leaf)) {
        throw "Missing tools\windows-register\requirements.txt in the package."
    }

    Write-Step "Installing/repairing register Python dependencies from requirements.txt..."
    # Always re-run pip install so half-broken venvs (missing cloakbrowser/playwright/etc.) self-heal.
    & $RegisterPythonPath -m pip install --upgrade pip setuptools wheel
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to upgrade pip inside tools\windows-register\.venv."
    }
    & $RegisterPythonPath -m pip install --upgrade -r $requirements
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to install tools\windows-register\requirements.txt. Ensure the machine can reach PyPI (or configure a mirror) and re-run deploy.bat install."
    }

    $missing = @(Get-MissingRegisterPythonModules)
    if ($missing.Count -gt 0) {
        throw ("Register Python packages still missing after pip install: {0}" -f ($missing -join ", "))
    }
    Write-Step "Register Python packages are installed: cloakbrowser, requests, python-dotenv, httpx, playwright, curl_cffi."
}

function Install-RegisterCloakBrowser {
    $browser = Get-RegisterBrowserPath
    if (-not [string]::IsNullOrWhiteSpace($browser) -and (Test-Path -LiteralPath $browser -PathType Leaf)) {
        Write-RegisterBrowserMarker $browser
        Write-Step ("CloakBrowser Chromium already present: {0}" -f $browser)
        return $browser
    }

    $oldHome = $env:HOME
    $oldUserProfile = $env:USERPROFILE
    $oldLocalAppData = $env:LOCALAPPDATA
    $oldPythonPath = $env:PYTHONPATH
    $oldPythonUtf8 = $env:PYTHONUTF8
    try {
        # Install Chromium under the package tree so LOCAL SERVICE / scheduled tasks can read it.
        $env:HOME = $RegisterEnginePath
        $env:USERPROFILE = $RegisterEnginePath
        $env:LOCALAPPDATA = Join-Path $RegisterEnginePath "AppData\Local"
        $env:PYTHONPATH = $RegisterEnginePath
        $env:PYTHONUTF8 = "1"
        [System.IO.Directory]::CreateDirectory($env:LOCALAPPDATA) | Out-Null

        Write-Step "Downloading CloakBrowser Chromium into the package (this may take several minutes)..."
        & $RegisterPythonPath -m cloakbrowser install
        if ($LASTEXITCODE -ne 0) {
            throw "CloakBrowser Chromium installation failed. Check network access and re-run deploy.bat install."
        }

        $browserPath = ""
        try {
            $browserFromPython = & $RegisterPythonPath -c "from grok_register.register import find_chrome; print(find_chrome())" 2>&1
            if ($LASTEXITCODE -eq 0) {
                $browserPath = ([string]$browserFromPython).Trim()
                if ($browserPath -match "[\r\n]") {
                    $browserPath = (@($browserPath -split "[\r\n]+" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }) | Select-Object -Last 1).Trim()
                }
            }
            else {
                Write-WarningLine ("find_chrome() failed after install: {0}" -f (($browserFromPython | ForEach-Object { "$_" }) -join " "))
            }
        }
        catch {
            Write-WarningLine ("find_chrome() raised after install: {0}" -f $_.Exception.Message)
        }

        if ([string]::IsNullOrWhiteSpace($browserPath) -or -not (Test-Path -LiteralPath $browserPath -PathType Leaf)) {
            $browserPath = Get-RegisterBrowserPath
        }
        if ([string]::IsNullOrWhiteSpace($browserPath) -or -not (Test-Path -LiteralPath $browserPath -PathType Leaf)) {
            throw "CloakBrowser chrome.exe was not found after installation under tools\windows-register\.cloakbrowser."
        }
        Write-RegisterBrowserMarker $browserPath
        Write-Step ("CloakBrowser Chromium ready: {0}" -f $browserPath)
        return $browserPath
    }
    finally {
        if ($null -eq $oldHome) { Remove-Item Env:HOME -ErrorAction SilentlyContinue } else { $env:HOME = $oldHome }
        if ($null -eq $oldUserProfile) { Remove-Item Env:USERPROFILE -ErrorAction SilentlyContinue } else { $env:USERPROFILE = $oldUserProfile }
        if ($null -eq $oldLocalAppData) { Remove-Item Env:LOCALAPPDATA -ErrorAction SilentlyContinue } else { $env:LOCALAPPDATA = $oldLocalAppData }
        if ($null -eq $oldPythonPath) { Remove-Item Env:PYTHONPATH -ErrorAction SilentlyContinue } else { $env:PYTHONPATH = $oldPythonPath }
        if ($null -eq $oldPythonUtf8) { Remove-Item Env:PYTHONUTF8 -ErrorAction SilentlyContinue } else { $env:PYTHONUTF8 = $oldPythonUtf8 }
    }
}

function Ensure-WindowsRegisterRuntime {
    param([switch]$Required)

    if (-not (Test-Path -LiteralPath (Join-Path $RegisterEnginePath "grok_register\register.py") -PathType Leaf)) {
        Write-WarningLine "Windows register engine is not packaged under tools\windows-register. Registration UI will stay unavailable."
        if ($Required) {
            throw "Windows register engine is missing from the package."
        }
        return
    }

    [System.IO.Directory]::CreateDirectory($RegisterOutputPath) | Out-Null

    if (Test-WindowsRegisterRuntimeReady) {
        $browser = Get-RegisterBrowserPath
        Write-RegisterBrowserMarker $browser
        Write-Step "Windows register runtime is ready (venv packages + CloakBrowser Chromium)."
        return
    }

    if (-not (Test-RegisterEngineWritable)) {
        Write-WarningLine "Windows register runtime is incomplete and tools\windows-register is not writable. Run deploy.bat install from an elevated prompt, or extract the package to a user-writable folder."
        if ($Required) {
            throw "Windows register runtime is not ready (package directory not writable)."
        }
        return
    }
    if (-not (Test-IsAdministratorAccount)) {
        Write-WarningLine "Current process is not elevated. Attempting automatic register runtime setup in the package folder..."
    }

    $hostPython = Resolve-HostPython
    if ([string]::IsNullOrWhiteSpace($hostPython)) {
        Write-WarningLine "Python 3.10+ was not found. Install Python from https://www.python.org/downloads/ (enable Add python.exe to PATH), then re-run deploy.bat install."
        Write-WarningLine "Core API still works. Only Windows browser registration needs Python + requirements.txt + CloakBrowser."
        if ($Required) {
            throw "Python 3.10+ is required for the Windows registration worker."
        }
        return
    }

    Write-Step ("Preparing Windows register runtime with host Python: {0}" -f $hostPython)
    Install-RegisterPythonPackages -HostPython $hostPython
    [void](Install-RegisterCloakBrowser)

    if (-not (Test-WindowsRegisterRuntimeReady)) {
        $missing = @(Get-MissingRegisterPythonModules)
        $browser = Get-RegisterBrowserPath
        $detail = @()
        if ($missing.Count -gt 0) { $detail += ("missing packages: " + ($missing -join ", ")) }
        if ([string]::IsNullOrWhiteSpace($browser)) { $detail += "browser not found" }
        throw ("Windows register runtime is still incomplete after automatic setup. {0}" -f ($detail -join "; "))
    }
    Write-Step "Windows register runtime prepared automatically. No manual cloakbrowser install is required."
}

function Set-WindowsRegisterProcessEnvironment {
    if (Test-Path -LiteralPath $RegisterEnginePath -PathType Container) {
        $env:GROK2API_REGISTER_ENGINE_PATH = $RegisterEnginePath
        # 让托管进程内的 python -m grok_register.* 在任意 cwd 下都能解析模块。
        $env:PYTHONPATH = $RegisterEnginePath
        $env:PYTHONUTF8 = "1"
    }
    $env:GROK2API_WINDOWS_REGISTER_DIR = $RegisterOutputPath
    if (Test-Path -LiteralPath $RegisterPythonPath -PathType Leaf) {
        $env:GROK2API_REGISTER_PYTHON = $RegisterPythonPath
    }
    $browser = Get-RegisterBrowserPath
    if (-not [string]::IsNullOrWhiteSpace($browser)) {
        $env:CLOAKBROWSER_EXECUTABLE_PATH = $browser
    }
}

function Assert-ExistingConfig {
    $content = [System.IO.File]::ReadAllText($ConfigPath)
    $placeholders = @(
        "replace-with-at-least-32-characters",
        "replace-with-base64-key",
        "replace-with-a-strong-password"
    )
    foreach ($placeholder in $placeholders) {
        if ($content.Contains($placeholder)) {
            throw "Existing config.yaml still contains example secrets. It was not modified to protect any existing data. Back it up, then fix it manually or remove it only if this is a new deployment."
        }
    }
}

function Initialize-Config {
    if (Test-Path -LiteralPath $ConfigPath -PathType Leaf) {
        Assert-ExistingConfig
        Write-Step "Existing config.yaml preserved."
        return $false
    }

    $existingData = Get-ChildItem -LiteralPath $DataPath -Force -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -ne $existingData -or (Test-Path -LiteralPath $CredentialsPath -PathType Leaf)) {
        throw "config.yaml is missing but existing runtime data was found. Restore the original config.yaml (especially credentialEncryptionKey); do not generate a new key for an existing database or media directory."
    }

    $template = [System.IO.File]::ReadAllText($ConfigTemplatePath, [Text.Encoding]::UTF8)
    foreach ($requiredPlaceholder in @(
        "replace-with-at-least-32-characters",
        "replace-with-base64-key",
        "replace-with-a-strong-password"
    )) {
        if (-not $template.Contains($requiredPlaceholder)) {
            throw "config.example.yaml does not contain the expected placeholder: $requiredPlaceholder"
        }
    }

    $adminPassword = New-AdminPassword
    $content = $template.Replace("replace-with-at-least-32-characters", (New-HexSecret))
    $content = $content.Replace("replace-with-base64-key", (New-Base64Secret))
    $content = $content.Replace("replace-with-a-strong-password", $adminPassword)
    $utf8NoBom = New-Object Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($ConfigPath, $content, $utf8NoBom)

    $credentialText = @"
Grok2API first-run administrator
================================
URL:      http://127.0.0.1:$Port
Username: admin
Password: $adminPassword
Created:  $([DateTime]::Now.ToString("yyyy-MM-dd HH:mm:ss zzz"))

Change the password after first login, then delete this file.
Keep config.yaml and data together when backing up or upgrading.
"@
    [System.IO.File]::WriteAllText($CredentialsPath, $credentialText, $utf8NoBom)
    try {
        Protect-SensitiveFile $ConfigPath -AllowLocalServiceRead
        Protect-SensitiveFile $CredentialsPath
    }
    catch {
        Remove-Item -LiteralPath $ConfigPath -Force -ErrorAction SilentlyContinue
        Remove-Item -LiteralPath $CredentialsPath -Force -ErrorAction SilentlyContinue
        throw "Could not protect first-run secrets with Windows ACLs: $($_.Exception.Message)"
    }
    Write-Step "Created config.yaml with cryptographically secure secrets."
    Write-WarningLine "Initial credentials were written to: $CredentialsPath"
    return $true
}

function Resolve-Port {
    if ($Port -gt 0) {
        return $Port
    }
    if (-not [string]::IsNullOrWhiteSpace($env:GROK2API_PORT)) {
        $parsed = 0
        if (-not [int]::TryParse($env:GROK2API_PORT, [ref]$parsed) -or $parsed -lt 1 -or $parsed -gt 65535) {
            throw "GROK2API_PORT must be between 1 and 65535."
        }
        return $parsed
    }
    if (Test-Path -LiteralPath $PortPath -PathType Leaf) {
        $saved = 0
        $raw = ([System.IO.File]::ReadAllText($PortPath)).Trim()
        if ([int]::TryParse($raw, [ref]$saved) -and $saved -ge 1 -and $saved -le 65535) {
            return $saved
        }
    }
    return 8000
}

function Save-Port {
    param([int]$Value)
    [System.IO.File]::WriteAllText($PortPath, $Value.ToString(), [Text.Encoding]::ASCII)
}

function Get-ManagedProcess {
    if (-not (Test-Path -LiteralPath $PidPath -PathType Leaf)) {
        return $null
    }
    $processId = 0
    $raw = ([System.IO.File]::ReadAllText($PidPath)).Trim()
    if (-not [int]::TryParse($raw, [ref]$processId)) {
        Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
        return $null
    }
    $process = Get-Process -Id $processId -ErrorAction SilentlyContinue
    if ($null -eq $process) {
        Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
        return $null
    }
    try {
        $actualPath = [System.IO.Path]::GetFullPath($process.Path)
    }
    catch {
        return $null
    }
    if (-not $actualPath.Equals([System.IO.Path]::GetFullPath($ExecutablePath), [StringComparison]::OrdinalIgnoreCase)) {
        Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
        return $null
    }
    return $process
}

function Test-Health {
    param([int]$Value)
    $response = $null
    try {
        $request = [Net.HttpWebRequest]::Create("http://127.0.0.1:$Value/healthz")
        $request.Proxy = $null
        $request.Timeout = 2000
        $request.ReadWriteTimeout = 2000
        $response = $request.GetResponse()
        return ([int]$response.StatusCode -eq 200)
    }
    catch {
        return $false
    }
    finally {
        if ($null -ne $response) {
            $response.Close()
        }
    }
}

function Test-ProcessOwnsPort {
    param([System.Diagnostics.Process]$Process, [int]$Value)
    if ($null -eq $Process) {
        return $false
    }
    try {
        $listeners = @(Get-NetTCPConnection -State Listen -LocalPort $Value -ErrorAction Stop)
        return $null -ne ($listeners | Where-Object { $_.OwningProcess -eq $Process.Id } | Select-Object -First 1)
    }
    catch {
        return $false
    }
}

function Assert-PortAvailable {
    param([int]$Value)
    $listener = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Any, $Value)
    try {
        $listener.Start()
    }
    catch {
        throw "Port $Value is already in use or unavailable."
    }
    finally {
        $listener.Stop()
    }
}

function Wait-ForHealth {
    param([int]$Value, [int]$TimeoutSeconds = 45)
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    do {
        if (Test-Health $Value) {
            return $true
        }
        Start-Sleep -Milliseconds 500
    } while ([DateTime]::UtcNow -lt $deadline)
    return $false
}

function Show-RecentErrors {
    if (Test-Path -LiteralPath $StderrPath -PathType Leaf) {
        Write-Host ""
        Write-Host "Last error log lines:"
        Get-Content -LiteralPath $StderrPath -Tail 30
    }
}

function Rotate-LogFile {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return
    }
    if ((Get-Item -LiteralPath $Path).Length -eq 0) {
        return
    }
    $archivePath = $Path + "." + [DateTime]::Now.ToString("yyyyMMdd-HHmmss-fff")
    Move-Item -LiteralPath $Path -Destination $archivePath
    $leaf = Split-Path -Leaf $Path
    $archives = Get-ChildItem -LiteralPath (Split-Path -Parent $Path) -File -Filter ($leaf + ".*") |
        Sort-Object LastWriteTime -Descending
    foreach ($archive in $archives | Select-Object -Skip 5) {
        Remove-Item -LiteralPath $archive.FullName -Force -ErrorAction SilentlyContinue
    }
}

function Start-ManagedProcess {
    param([int]$Value, [switch]$Wait)
    $existing = Get-ManagedProcess
    if ($null -ne $existing) {
        if ($Wait) {
            $existingExitCode = 1
            $existingProcessId = 0
            try {
                $existingProcessId = [int]$existing.Id
            }
            catch {
                $existingProcessId = 0
            }
            try {
                $existing.WaitForExit()
                if ($null -ne $existing.ExitCode) {
                    $existingExitCode = $existing.ExitCode
                }
            }
            catch {
                $existingExitCode = 1
            }
            finally {
                if ($existingProcessId -gt 0 -and (Test-Path -LiteralPath $PidPath -PathType Leaf)) {
                    $currentPid = ([System.IO.File]::ReadAllText($PidPath)).Trim()
                    if ($currentPid -eq $existingProcessId.ToString()) {
                        Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
                    }
                }
            }
            return $existingExitCode
        }
        return $existing
    }
    Assert-PortAvailable $Value
    Rotate-LogFile $StdoutPath
    Rotate-LogFile $StderrPath
    Rotate-LogFile $TaskLogPath
    Set-WindowsRegisterProcessEnvironment
    $argumentLine = '--config "{0}" --listen "0.0.0.0:{1}"' -f $ConfigPath, $Value
    $process = Start-Process `
        -FilePath $ExecutablePath `
        -ArgumentList $argumentLine `
        -WorkingDirectory $Root `
        -WindowStyle Hidden `
        -RedirectStandardOutput $StdoutPath `
        -RedirectStandardError $StderrPath `
        -PassThru
    $startedProcessId = $process.Id
    [System.IO.File]::WriteAllText($PidPath, $startedProcessId.ToString(), [Text.Encoding]::ASCII)
    Save-Port $Value
    if ($Wait) {
        $exitCode = 1
        try {
            $process.WaitForExit()
            if ($null -ne $process.ExitCode) {
                $exitCode = $process.ExitCode
            }
        }
        finally {
            $currentPid = ""
            if (Test-Path -LiteralPath $PidPath -PathType Leaf) {
                $currentPid = ([System.IO.File]::ReadAllText($PidPath)).Trim()
            }
            if ($currentPid -eq $startedProcessId.ToString()) {
                Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
            }
        }
        return $exitCode
    }
    return $process
}

function Get-ServiceTask {
    try {
        $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction Stop
    }
    catch {
        if ($_.FullyQualifiedErrorId -like "CmdletizationQuery_NotFound_TaskName,*") {
            return $null
        }
        throw "Could not query Windows startup task '$TaskName': $($_.Exception.Message)"
    }
    $expectedScript = '-File "' + $ScriptFile + '"'
    $expectedRoot = '-AppRoot "' + $Root + '"'
    $owned = $false
    foreach ($taskAction in @($task.Actions)) {
        if ($null -eq $taskAction -or [string]::IsNullOrWhiteSpace([string]$taskAction.Arguments)) {
            continue
        }
        $arguments = [string]$taskAction.Arguments
        if ($arguments.IndexOf($expectedScript, [StringComparison]::OrdinalIgnoreCase) -ge 0 -and
            $arguments.IndexOf($expectedRoot, [StringComparison]::OrdinalIgnoreCase) -ge 0) {
            $owned = $true
            break
        }
    }
    if (-not $owned) {
        throw "Scheduled task '$TaskName' exists but is not owned by this deployment directory. It was not modified."
    }
    return $task
}

function Assert-ServiceTaskInstalled {
    if ($null -eq (Get-ServiceTask)) {
        throw "The startup task is not installed. Run deploy.bat install [port] first."
    }
}

function Install-ServiceTask {
    param([int]$Value)
    Import-Module ScheduledTasks -ErrorAction Stop
    Get-ServiceTask | Out-Null
    Protect-DeploymentDirectory
    $powerShellPath = Join-Path $PSHOME "powershell.exe"
    $taskArguments = '-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "{0}" -AppRoot "{1}" -Action run-task' -f $ScriptFile, $Root
    $taskAction = New-ScheduledTaskAction -Execute $powerShellPath -Argument $taskArguments -WorkingDirectory $Root
    $trigger = New-ScheduledTaskTrigger -AtStartup
    $localService = (New-Object Security.Principal.SecurityIdentifier("S-1-5-19")).Translate([Security.Principal.NTAccount]).Value
    $principal = New-ScheduledTaskPrincipal -UserId $localService -LogonType ServiceAccount
    $settings = New-ScheduledTaskSettingsSet `
        -AllowStartIfOnBatteries `
        -DontStopIfGoingOnBatteries `
        -StartWhenAvailable `
        -RestartCount 3 `
        -RestartInterval (New-TimeSpan -Minutes 1) `
        -ExecutionTimeLimit ([TimeSpan]::Zero)
    $task = New-ScheduledTask -Action $taskAction -Trigger $trigger -Principal $principal -Settings $settings
    Register-ScheduledTask -TaskName $TaskName -InputObject $task -Force | Out-Null
    Save-Port $Value
    Write-Step "Installed Windows startup task '$TaskName'."
}

function Stop-Application {
    $task = Get-ServiceTask
    if ($null -ne $task -and $task.State.ToString() -in @("Running", "Queued")) {
        try {
            Stop-ScheduledTask -TaskName $TaskName -ErrorAction Stop
        }
        catch {
            $currentTask = Get-ServiceTask
            if ($null -ne $currentTask -and $currentTask.State.ToString() -in @("Running", "Queued")) {
                throw "Could not stop startup task '$TaskName': $($_.Exception.Message)"
            }
        }
        $taskDeadline = [DateTime]::UtcNow.AddSeconds(10)
        do {
            Start-Sleep -Milliseconds 250
            $currentTask = Get-ServiceTask
            if ($null -eq $currentTask -or $currentTask.State.ToString() -notin @("Running", "Queued")) {
                break
            }
        } while ([DateTime]::UtcNow -lt $taskDeadline)
        if ($null -ne $currentTask -and $currentTask.State.ToString() -in @("Running", "Queued")) {
            throw "Startup task '$TaskName' did not stop within 10 seconds."
        }
    }
    $process = Get-ManagedProcess
    if ($null -ne $process) {
        $processId = 0
        try {
            $processId = [int]$process.Id
        }
        catch {
            $processId = 0
        }
        if ($processId -gt 0) {
            try {
                Stop-Process -Id $processId -Force -ErrorAction Stop
            }
            catch {
                # The scheduled task may have already terminated the child between lookup and stop.
            }
            $deadline = [DateTime]::UtcNow.AddSeconds(5)
            do {
                $remaining = Get-Process -Id $processId -ErrorAction SilentlyContinue
                if ($null -eq $remaining) {
                    break
                }
                Start-Sleep -Milliseconds 250
            } while ([DateTime]::UtcNow -lt $deadline)
            if ($null -ne $remaining) {
                try {
                    $remainingPath = [System.IO.Path]::GetFullPath($remaining.Path)
                }
                catch {
                    throw "Could not verify the remaining process $processId before forced stop."
                }
                if (-not $remainingPath.Equals([System.IO.Path]::GetFullPath($ExecutablePath), [StringComparison]::OrdinalIgnoreCase)) {
                    throw "PID $processId no longer belongs to this deployment; it was not stopped."
                }
                Stop-Process -Id $processId -Force -ErrorAction Stop
                Start-Sleep -Milliseconds 500
                if ($null -ne (Get-Process -Id $processId -ErrorAction SilentlyContinue)) {
                    throw "Grok2API process $processId did not stop."
                }
            }
        }
    }
    $task = Get-ServiceTask
    if ($null -ne $task -and $task.State.ToString() -in @("Running", "Queued")) {
        throw "Startup task '$TaskName' became active while stopping; retry the stop operation."
    }
    Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
    Write-Step "Application stopped."
}

function Start-Application {
    param([int]$Value)
    $process = Get-ManagedProcess
    if ($null -ne $process) {
        $savedPort = 0
        if (Test-Path -LiteralPath $PortPath -PathType Leaf) {
            [int]::TryParse(([System.IO.File]::ReadAllText($PortPath)).Trim(), [ref]$savedPort) | Out-Null
        }
        if ($savedPort -eq $Value -and (Test-ProcessOwnsPort $process $Value) -and (Test-Health $Value)) {
            Write-Step "Already running (PID $($process.Id))."
            return
        }
        throw "Grok2API is already running but is not healthy on its saved port. Use deploy.bat restart [port]."
    }
    $task = Get-ServiceTask
    if ($null -eq $task) {
        throw "The startup task is not installed. Run deploy.bat install [port] first."
    }
    Save-Port $Value
    Start-ScheduledTask -TaskName $TaskName
    if (-not (Wait-ForHealth $Value)) {
        Show-RecentErrors
        throw "The process did not pass /healthz within 45 seconds."
    }
    $process = Get-ManagedProcess
    if ($null -eq $process) {
        throw "The health endpoint responded, but no Grok2API process owned by this deployment was found."
    }
    if (-not (Test-ProcessOwnsPort $process $Value)) {
        throw "The health endpoint responded, but this deployment process does not own port $Value."
    }
    $pidText = $process.Id.ToString()
    Write-Step "Started successfully (PID $pidText)."
    Write-Host "Admin console: http://127.0.0.1:$Value"
    Write-Host "LAN/public bind: http://0.0.0.0:$Value"
    Write-Host "Readiness:      http://127.0.0.1:$Value/readyz"
    if (Test-Path -LiteralPath $CredentialsPath -PathType Leaf) {
        Write-WarningLine "Read the initial login from $CredentialsPath and delete it after changing the password."
    }
    Write-WarningLine "No firewall rule was opened automatically. Use HTTPS through a reverse proxy before public exposure."
}

function Show-Status {
    param([int]$Value)
    $task = Get-ServiceTask
    $process = Get-ManagedProcess
    $taskState = if ($null -eq $task) { "not installed" } else { $task.State.ToString() }
    $processState = if ($null -eq $process) { "stopped" } else { "running (PID $($process.Id))" }
    $healthState = if ($null -ne $process -and (Test-ProcessOwnsPort $process $Value) -and (Test-Health $Value)) { "healthy" } else { "unavailable" }
    Write-Host "Startup task: $taskState"
    if ($null -ne $task) {
        try {
            $taskInfo = Get-ScheduledTaskInfo -TaskName $TaskName -ErrorAction Stop
            Write-Host "Task result:  $($taskInfo.LastTaskResult)"
        }
        catch {
            Write-Host "Task result:  unavailable"
        }
    }
    Write-Host "Process:      $processState"
    Write-Host "Health:       $healthState"
    Write-Host "Port:         $Value"
    if ($null -eq $process -or $healthState -ne "healthy") {
        exit 1
    }
}

function Show-Logs {
    foreach ($logPath in @($TaskLogPath, $StderrPath, $StdoutPath)) {
        Write-Host ""
        Write-Host ("===== " + $logPath + " =====")
        if (Test-Path -LiteralPath $logPath -PathType Leaf) {
            Get-Content -LiteralPath $logPath -Tail 100
        }
        else {
            Write-Host "No log file yet."
        }
    }
}

function Show-Help {
    Write-Host @"
Grok2API Windows deployment

  deploy.bat                    Initialize, install startup task, and start
  deploy.bat install [port]     Install/update startup task and start
  deploy.bat start [port]       Start the installed startup task
  deploy.bat stop               Stop the application
  deploy.bat restart [port]     Restart the application
  deploy.bat status             Show task, process, and health status
  deploy.bat logs               Show the latest logs
  deploy.bat run [port]         Run in this console; press Ctrl+C to stop
  deploy.bat uninstall          Remove startup task; keep config and data

Default port: 8000. GROK2API_PORT may also set the port.
"@
}

try {
    $Port = Resolve-Port
    if ($Action -ne "help") {
        Assert-SafeAppRoot
    }
    if ($Action -in @("install", "start", "stop", "restart", "uninstall") -and -not (Test-IsAdministrator)) {
        Invoke-Elevated
    }

    switch ($Action) {
        "help" {
            Show-Help
        }
        "logs" {
            Show-Logs
        }
        "status" {
            Show-Status $Port
        }
        "stop" {
            Stop-Application
        }
        "uninstall" {
            Stop-Application
            if ($null -ne (Get-ServiceTask)) {
                Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
                Write-Step "Removed startup task '$TaskName'. Config and data were preserved."
            }
            else {
                Write-Step "Startup task is not installed. Config and data were preserved."
            }
        }
        "run-task" {
            Assert-DeploymentEnvironment
            Initialize-Config | Out-Null
            # Do not bootstrap or rewrite package files under LOCAL SERVICE.
            # deploy.bat install/start already prepared the register runtime as admin.
            Set-WindowsRegisterProcessEnvironment
            $serviceExitCode = Start-ManagedProcess -Value $Port -Wait
            throw "Grok2API exited with code $serviceExitCode."
        }
        "run" {
            Assert-DeploymentEnvironment
            Initialize-Config | Out-Null
            Ensure-WindowsRegisterRuntime
            [System.IO.Directory]::CreateDirectory($RegisterOutputPath) | Out-Null
            Set-WindowsRegisterProcessEnvironment
            Assert-PortAvailable $Port
            Save-Port $Port
            Write-Step "Running in this console on port $Port. Press Ctrl+C to stop."
            & $ExecutablePath --config $ConfigPath --listen "0.0.0.0:$Port"
            if ($LASTEXITCODE -ne 0) {
                throw "Grok2API exited with code $LASTEXITCODE."
            }
        }
        "start" {
            Assert-DeploymentEnvironment
            Assert-ServiceTaskInstalled
            Initialize-Config | Out-Null
            Ensure-WindowsRegisterRuntime
            Protect-DeploymentDirectory
            Start-Application $Port
        }
        "restart" {
            Assert-DeploymentEnvironment
            Assert-ServiceTaskInstalled
            Initialize-Config | Out-Null
            Ensure-WindowsRegisterRuntime
            Protect-DeploymentDirectory
            Stop-Application
            Start-Application $Port
        }
        "install" {
            Assert-DeploymentEnvironment
            Initialize-Config | Out-Null
            Ensure-WindowsRegisterRuntime
            Stop-Application
            Install-ServiceTask $Port
            Start-Application $Port
            if (Test-WindowsRegisterRuntimeReady) {
                Write-Step "Windows registration worker is ready in the admin UI under Registration."
                Write-Step "Python packages + CloakBrowser were prepared automatically under tools\windows-register\.venv."
            }
            else {
                # ZIP does not ship .venv/browser binaries (too large). deploy.bat must auto-install them on the target PC.
                # Core API is unaffected; only Windows browser registration needs Python + PyPI once.
                $missing = @(Get-MissingRegisterPythonModules)
                Write-WarningLine "Core service started, but Windows registration runtime is not ready."
                if ($missing.Count -gt 0) {
                    Write-WarningLine ("Missing Python packages in .venv: {0}" -f ($missing -join ", "))
                }
                if ([string]::IsNullOrWhiteSpace((Get-RegisterBrowserPath))) {
                    Write-WarningLine "CloakBrowser Chromium binary was not found under tools\windows-register\.cloakbrowser."
                }
                Write-WarningLine "Fix once: install Python 3.10+ (Add to PATH), ensure PyPI network access, then re-run deploy.bat install."
                Write-WarningLine "Do NOT run cloakbrowser install by hand unless automatic setup failed; deploy.bat is supposed to do it."
            }
        }
    }
}
catch {
    Write-Host ("[ERROR] " + $_.Exception.Message) -ForegroundColor Red
    if (-not [string]::IsNullOrWhiteSpace($_.ScriptStackTrace)) {
        Write-Host ("[ERROR] " + $_.ScriptStackTrace) -ForegroundColor DarkRed
    }
    if ($Action -eq "run-task") {
        try {
            [System.IO.Directory]::CreateDirectory($LogsPath) | Out-Null
            $entry = "[{0}] {1}{2}{3}{2}" -f [DateTime]::Now.ToString("yyyy-MM-dd HH:mm:ss zzz"), $_.Exception.Message, [Environment]::NewLine, $_.ScriptStackTrace
            [System.IO.File]::AppendAllText($TaskLogPath, $entry, (New-Object Text.UTF8Encoding($false)))
        }
        catch {
            # The console error above is the final fallback when task logging is unavailable.
        }
    }
    exit 1
}
