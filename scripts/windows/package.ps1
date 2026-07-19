[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet("amd64", "arm64", "all")]
    [string]$Architecture = "all",

    [switch]$SkipChecks
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = "Stop"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$ProjectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot "..\.."))
$ToolsRoot = Join-Path $ProjectRoot ".tools"
$DownloadsRoot = Join-Path $ToolsRoot "downloads"
$WorkRoot = Join-Path $ProjectRoot ".tmp\windows-package"
$ArtifactWorkRoot = Join-Path $WorkRoot "artifacts"
$ReleaseRoot = Join-Path $ProjectRoot "release"
$FrontendRoot = Join-Path $ProjectRoot "frontend"
$FrontendDist = Join-Path $FrontendRoot "dist"
$BackendRoot = Join-Path $ProjectRoot "backend"
$PnpmStore = Join-Path $ProjectRoot ".pnpm-store"
$RequiredGoVersion = [version]"1.26.0"
$RequiredNodeVersion = [version]"22.12.0"
$RequiredPnpmVersion = [version]"11.5.2"
$PortablePnpmVersion = "11.5.2"
$PortablePnpmUrl = "https://registry.npmjs.org/pnpm/-/pnpm-11.5.2.tgz"
$PortablePnpmSha512 = "71c631e382066efc25625d5cf029075de07b61b37f6e27350fbd84b1bda5864c8c1967adc280776b45c30a715c0359a3be08fef42d5bb09e2b99029979692916"
$BuildLockStream = $null
$PackageFailed = $false

function Write-Step {
    param([string]$Message)
    Write-Host ("[Grok2API] " + $Message) -ForegroundColor Cyan
}

function Write-Success {
    param([string]$Message)
    Write-Host ("[OK] " + $Message) -ForegroundColor Green
}

function Get-HostArchitecture {
    $value = $env:PROCESSOR_ARCHITEW6432
    if ([string]::IsNullOrWhiteSpace($value)) {
        $value = $env:PROCESSOR_ARCHITECTURE
    }
    switch ($value.ToUpperInvariant()) {
        "AMD64" { return "amd64" }
        "ARM64" { return "arm64" }
        default { throw "Packaging requires 64-bit Windows (amd64 or arm64). Detected: $value" }
    }
}

function Get-NodeArchitecture {
    param([string]$HostArchitecture)
    if ($HostArchitecture -eq "amd64") {
        return "x64"
    }
    return $HostArchitecture
}

function Convert-ToVersion {
    param([string]$Value, [string]$Prefix = "")
    $clean = $Value.Trim()
    if (-not [string]::IsNullOrEmpty($Prefix) -and $clean.StartsWith($Prefix)) {
        $clean = $clean.Substring($Prefix.Length)
    }
    $clean = $clean.Split("-")[0]
    try {
        return [version]$clean
    }
    catch {
        return $null
    }
}

function Get-CommandPath {
    param([string]$Name)
    $command = Get-Command $Name -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $command) {
        return $null
    }
    if (-not [string]::IsNullOrWhiteSpace($command.Source)) {
        return $command.Source
    }
    return $command.Path
}

function Get-GoVersion {
    param([string]$Path)
    try {
        $output = (& $Path version 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -ne 0 -or $output -notmatch "go([0-9]+\.[0-9]+(?:\.[0-9]+)?)") {
            return $null
        }
        return Convert-ToVersion $Matches[1]
    }
    catch {
        return $null
    }
}

function Get-NodeVersion {
    param([string]$Path)
    try {
        $output = (& $Path --version 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -ne 0) {
            return $null
        }
        return Convert-ToVersion $output "v"
    }
    catch {
        return $null
    }
}

function Get-PnpmVersion {
    param([string]$Path)
    try {
        $output = (& $Path --version 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -ne 0) {
            return $null
        }
        return Convert-ToVersion $output
    }
    catch {
        return $null
    }
}

function Test-GoVersion {
    param([version]$Value)
    return $null -ne $Value -and $Value -ge $RequiredGoVersion
}

function Test-GoToolchain {
    param([string]$Path)
    $version = Get-GoVersion $Path
    if (-not (Test-GoVersion $version)) {
        return $false
    }
    $goRoot = [System.IO.Path]::GetFullPath((Join-Path (Split-Path -Parent $Path) ".."))
    return (Test-Path -LiteralPath (Join-Path $goRoot "src\runtime\runtime1.go") -PathType Leaf) -and
        (Test-Path -LiteralPath (Join-Path $goRoot "src\net\http\server.go") -PathType Leaf) -and
        ($null -ne (Get-ChildItem -LiteralPath (Join-Path $goRoot "pkg\tool") -Recurse -File -Filter "compile.exe" -ErrorAction SilentlyContinue | Select-Object -First 1))
}

function Test-NodeVersion {
    param([version]$Value)
    return $null -ne $Value -and $Value -ge $RequiredNodeVersion
}

function Test-PnpmVersion {
    param([version]$Value)
    return $null -ne $Value -and $Value.Major -eq 11 -and $Value -ge $RequiredPnpmVersion
}

function Assert-NoReparsePath {
    param([string]$Path, [string]$Boundary)
    $full = [System.IO.Path]::GetFullPath($Path)
    $boundaryFull = [System.IO.Path]::GetFullPath($Boundary).TrimEnd("\")
    if (-not ($full.Equals($boundaryFull, [StringComparison]::OrdinalIgnoreCase) -or
        $full.StartsWith($boundaryFull + "\", [StringComparison]::OrdinalIgnoreCase))) {
        throw "Path is outside its safety boundary: $full"
    }
    $current = $full
    while ($current.Length -ge $boundaryFull.Length) {
        if (Test-Path -LiteralPath $current) {
            $item = Get-Item -LiteralPath $current -Force
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Refusing to modify a junction or symbolic-link path: $current"
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

function Assert-NoReparseTree {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Container)) {
        return
    }
    $link = Get-ChildItem -LiteralPath $Path -Recurse -Force -ErrorAction Stop |
        Where-Object { ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 } |
        Select-Object -First 1
    if ($null -ne $link) {
        throw "Refusing to recursively modify a tree containing a junction or symbolic link: $($link.FullName)"
    }
}

function Assert-ToolsChildPath {
    param([string]$Path)
    $full = [System.IO.Path]::GetFullPath($Path)
    $prefix = [System.IO.Path]::GetFullPath($ToolsRoot).TrimEnd("\") + "\"
    if (-not $full.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to modify a path outside .tools: $full"
    }
    Assert-NoReparsePath $full $ProjectRoot
    return $full
}

function Reset-ToolsDirectory {
    param([string]$Path)
    $full = Assert-ToolsChildPath $Path
    if (Test-Path -LiteralPath $full) {
        Assert-NoReparseTree $full
        Remove-Item -LiteralPath $full -Recurse -Force
    }
    [System.IO.Directory]::CreateDirectory($full) | Out-Null
}

function Get-FileSha256 {
    param([string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Get-FileSha512 {
    param([string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA512).Hash.ToLowerInvariant()
}

function Get-VerifiedDownload {
    param(
        [string]$Url,
        [string]$Destination,
        [string]$ExpectedSha256
    )
    $expected = $ExpectedSha256.Trim().ToLowerInvariant()
    if (Test-Path -LiteralPath $Destination -PathType Leaf) {
        if ((Get-FileSha256 $Destination) -eq $expected) {
            return $Destination
        }
        Remove-Item -LiteralPath (Assert-ToolsChildPath $Destination) -Force
    }
    $partial = Assert-ToolsChildPath ($Destination + ".partial")
    Remove-Item -LiteralPath $partial -Force -ErrorAction SilentlyContinue
    Write-Step "Downloading $Url"
    Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $partial
    $actual = Get-FileSha256 $partial
    if ($actual -ne $expected) {
        Remove-Item -LiteralPath $partial -Force -ErrorAction SilentlyContinue
        throw "SHA-256 mismatch for $Url"
    }
    Move-Item -LiteralPath $partial -Destination $Destination -Force
    return $Destination
}

function Find-CachedGo {
    if (-not (Test-Path -LiteralPath $ToolsRoot -PathType Container)) {
        return $null
    }
    foreach ($directory in Get-ChildItem -LiteralPath $ToolsRoot -Directory -Filter "go-*" | Where-Object { -not $_.Name.EndsWith(".partial") }) {
        $candidate = Join-Path $directory.FullName "go\bin\go.exe"
        if (Test-GoToolchain $candidate) {
            return $candidate
        }
    }
    return $null
}

function Install-PortableGo {
    param([string]$HostArchitecture)
    Write-Step "Go 1.26+ was not found. Installing a verified portable Go toolchain under .tools..."
    $releases = Invoke-RestMethod -UseBasicParsing -Uri "https://go.dev/dl/?mode=json"
    $selectedRelease = $null
    $selectedFile = $null
    foreach ($release in $releases) {
        $parsed = Convert-ToVersion $release.version "go"
        if (-not $release.stable -or $null -eq $parsed -or $parsed -lt $RequiredGoVersion) {
            continue
        }
        $file = $release.files | Where-Object {
            $_.os -eq "windows" -and $_.arch -eq $HostArchitecture -and $_.kind -eq "archive"
        } | Select-Object -First 1
        if ($null -ne $file) {
            $selectedRelease = $release
            $selectedFile = $file
            break
        }
    }
    if ($null -eq $selectedFile) {
        throw "No compatible stable Go 1.26+ archive was found for windows/$HostArchitecture."
    }
    $zipPath = Join-Path $DownloadsRoot $selectedFile.filename
    Get-VerifiedDownload -Url ("https://go.dev/dl/" + $selectedFile.filename) -Destination $zipPath -ExpectedSha256 $selectedFile.sha256 | Out-Null
    $installRoot = Join-Path $ToolsRoot ("go-" + $selectedRelease.version + "-windows-" + $HostArchitecture)
    $partialRoot = $installRoot + ".partial"
    Reset-ToolsDirectory $partialRoot
    Expand-Archive -LiteralPath $zipPath -DestinationPath $partialRoot -Force
    $partialGoPath = Join-Path $partialRoot "go\bin\go.exe"
    if (-not (Test-GoToolchain $partialGoPath)) {
        throw "Portable Go installation failed: $partialGoPath"
    }
    if (Test-Path -LiteralPath $installRoot) {
        $safeInstallRoot = Assert-ToolsChildPath $installRoot
        Assert-NoReparseTree $safeInstallRoot
        Remove-Item -LiteralPath $safeInstallRoot -Recurse -Force
    }
    Move-Item -LiteralPath $partialRoot -Destination $installRoot
    $goPath = Join-Path $installRoot "go\bin\go.exe"
    return $goPath
}

function Resolve-Go {
    param([string]$HostArchitecture)
    $systemPath = Get-CommandPath "go.exe"
    if ($null -ne $systemPath) {
        $version = Get-GoVersion $systemPath
        if (Test-GoToolchain $systemPath) {
            Write-Success "Using Go $version from $systemPath"
            return $systemPath
        }
    }
    $cached = Find-CachedGo
    if ($null -ne $cached) {
        Write-Success "Using cached Go $(Get-GoVersion $cached) from $cached"
        return $cached
    }
    return Install-PortableGo $HostArchitecture
}

function Find-CachedNode {
    if (-not (Test-Path -LiteralPath $ToolsRoot -PathType Container)) {
        return $null
    }
    foreach ($directory in Get-ChildItem -LiteralPath $ToolsRoot -Directory -Filter "node-*" | Where-Object { -not $_.Name.EndsWith(".partial") }) {
        $candidate = Get-ChildItem -LiteralPath $directory.FullName -Recurse -File -Filter "node.exe" -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($null -ne $candidate) {
            $version = Get-NodeVersion $candidate.FullName
            if (Test-NodeVersion $version) {
                return $candidate.FullName
            }
        }
    }
    return $null
}

function Install-PortableNode {
    param([string]$HostArchitecture)
    Write-Step "Node.js 22.12+ was not found. Installing a verified portable Node.js 22 toolchain under .tools..."
    $nodeArchitecture = Get-NodeArchitecture $HostArchitecture
    $nodeReleases = Invoke-RestMethod -UseBasicParsing -Uri "https://nodejs.org/dist/index.json"
    $release = $nodeReleases | Where-Object {
            $_.version -match "^v22\." -and $_.files -contains ("win-" + $nodeArchitecture + "-zip")
        } |
        Select-Object -First 1
    if ($null -eq $release) {
        throw "No compatible Node.js 22 archive was found for windows/$HostArchitecture."
    }
    $baseName = "node-$($release.version)-win-$nodeArchitecture"
    $fileName = $baseName + ".zip"
    $baseUrl = "https://nodejs.org/dist/$($release.version)"
    $checksumContent = (Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/SHASUMS256.txt").Content
    $match = [regex]::Match($checksumContent, ("(?m)^([0-9a-fA-F]{64})\s+\*?" + [regex]::Escape($fileName) + "\s*$"))
    if (-not $match.Success) {
        throw "Could not find $fileName in the official Node.js checksum list."
    }
    $zipPath = Join-Path $DownloadsRoot $fileName
    Get-VerifiedDownload -Url "$baseUrl/$fileName" -Destination $zipPath -ExpectedSha256 $match.Groups[1].Value | Out-Null
    $installRoot = Join-Path $ToolsRoot ("node-" + $release.version + "-windows-" + $HostArchitecture)
    $partialRoot = $installRoot + ".partial"
    Reset-ToolsDirectory $partialRoot
    Expand-Archive -LiteralPath $zipPath -DestinationPath $partialRoot -Force
    $partialNodePath = Join-Path $partialRoot "$baseName\node.exe"
    if (-not (Test-NodeVersion (Get-NodeVersion $partialNodePath))) {
        throw "Portable Node.js installation failed: $partialNodePath"
    }
    if (Test-Path -LiteralPath $installRoot) {
        $safeInstallRoot = Assert-ToolsChildPath $installRoot
        Assert-NoReparseTree $safeInstallRoot
        Remove-Item -LiteralPath $safeInstallRoot -Recurse -Force
    }
    Move-Item -LiteralPath $partialRoot -Destination $installRoot
    $nodePath = Join-Path $installRoot "$baseName\node.exe"
    return $nodePath
}

function Resolve-Node {
    param([string]$HostArchitecture)
    $systemPath = Get-CommandPath "node.exe"
    if ($null -ne $systemPath) {
        $version = Get-NodeVersion $systemPath
        if (Test-NodeVersion $version) {
            Write-Success "Using Node.js $version from $systemPath"
            return $systemPath
        }
    }
    $cached = Find-CachedNode
    if ($null -ne $cached) {
        Write-Success "Using cached Node.js $(Get-NodeVersion $cached) from $cached"
        return $cached
    }
    return Install-PortableNode $HostArchitecture
}

function Resolve-Pnpm {
    param([string]$NodePath)
    $systemPath = Get-CommandPath "pnpm.cmd"
    if ($null -ne $systemPath) {
        $version = Get-PnpmVersion $systemPath
        if (Test-PnpmVersion $version) {
            Write-Success "Using pnpm $version from $systemPath"
            return $systemPath
        }
    }

    $prefix = Join-Path $ToolsRoot ("pnpm-" + $PortablePnpmVersion + "-verified")
    $cachedPath = Join-Path $prefix "pnpm.cmd"
    if (Test-PnpmVersion (Get-PnpmVersion $cachedPath)) {
        Write-Success "Using cached pnpm $(Get-PnpmVersion $cachedPath) from $cachedPath"
        return $cachedPath
    }

    $npmPath = Join-Path (Split-Path -Parent $NodePath) "npm.cmd"
    if (-not (Test-Path -LiteralPath $npmPath -PathType Leaf)) {
        $npmPath = Get-CommandPath "npm.cmd"
    }
    if ($null -eq $npmPath) {
        throw "npm.cmd was not found next to Node.js."
    }
    Write-Step "pnpm 11.5.2+ was not found. Installing a verified pnpm $PortablePnpmVersion under .tools..."
    $tarballPath = Join-Path $DownloadsRoot ("pnpm-" + $PortablePnpmVersion + ".tgz")
    if (Test-Path -LiteralPath $tarballPath -PathType Leaf) {
        if ((Get-FileSha512 $tarballPath) -ne $PortablePnpmSha512) {
            Remove-Item -LiteralPath (Assert-ToolsChildPath $tarballPath) -Force
        }
    }
    if (-not (Test-Path -LiteralPath $tarballPath -PathType Leaf)) {
        $partialTarball = Assert-ToolsChildPath ($tarballPath + ".partial")
        Remove-Item -LiteralPath $partialTarball -Force -ErrorAction SilentlyContinue
        Write-Step "Downloading $PortablePnpmUrl"
        Invoke-WebRequest -UseBasicParsing -Uri $PortablePnpmUrl -OutFile $partialTarball
        if ((Get-FileSha512 $partialTarball) -ne $PortablePnpmSha512) {
            Remove-Item -LiteralPath $partialTarball -Force -ErrorAction SilentlyContinue
            throw "SHA-512 mismatch for $PortablePnpmUrl"
        }
        Move-Item -LiteralPath $partialTarball -Destination $tarballPath
    }

    $partialPrefix = $prefix + ".partial"
    Reset-ToolsDirectory $partialPrefix
    & $npmPath install --global --prefix $partialPrefix $tarballPath --ignore-scripts --offline --bin-links=true --no-fund --no-audit | Out-Host
    $partialPnpmPath = Join-Path $partialPrefix "pnpm.cmd"
    if ($LASTEXITCODE -ne 0 -or -not (Test-PnpmVersion (Get-PnpmVersion $partialPnpmPath))) {
        throw "Portable pnpm installation failed."
    }
    if (Test-Path -LiteralPath $prefix) {
        $safePrefix = Assert-ToolsChildPath $prefix
        Assert-NoReparseTree $safePrefix
        Remove-Item -LiteralPath $safePrefix -Recurse -Force
    }
    Move-Item -LiteralPath $partialPrefix -Destination $prefix
    return $cachedPath
}

function Invoke-Checked {
    param(
        [string]$FilePath,
        [string[]]$Arguments,
        [string]$WorkingDirectory
    )
    Push-Location $WorkingDirectory
    try {
        & $FilePath @Arguments
        if ($LASTEXITCODE -ne 0) {
            throw "Command failed with exit code ${LASTEXITCODE}: $FilePath $($Arguments -join ' ')"
        }
    }
    finally {
        Pop-Location
    }
}

function Assert-ConfigTemplateSafe {
    $templatePath = Join-Path $ProjectRoot "config.example.yaml"
    $content = [System.IO.File]::ReadAllText($templatePath, [Text.Encoding]::UTF8)
    $requiredLines = @(
        '(?m)^\s*jwtSecret:\s*"replace-with-at-least-32-characters"\s*$',
        '(?m)^\s*credentialEncryptionKey:\s*"replace-with-base64-key"\s*$',
        '(?m)^\s*password:\s*"replace-with-a-strong-password"\s*$'
    )
    foreach ($pattern in $requiredLines) {
        if (-not [regex]::IsMatch($content, $pattern)) {
            throw "config.example.yaml must contain only the documented secret placeholders before packaging. Refusing to risk publishing a real secret."
        }
    }

    $sensitiveLines = @($content -split "`r?`n" | ForEach-Object { $_.Trim() } | Where-Object {
        -not $_.StartsWith("#") -and $_ -match "(?i)(secret|password|token|credential|dsn|api.?key|private.?key|username)"
    })
    $expectedSensitiveLines = @(
        "accessTokenTTL: 15m",
        "refreshTokenTTL: 720h",
        "secrets:",
        'jwtSecret: "replace-with-at-least-32-characters"',
        'credentialEncryptionKey: "replace-with-base64-key"',
        'username: "admin"',
        'password: "replace-with-a-strong-password"',
        'dsn: "postgres://user:password@127.0.0.1:5432/grok2api?sslmode=disable"',
        'username: ""',
        'password: ""'
    )
    if ($sensitiveLines.Count -ne $expectedSensitiveLines.Count) {
        throw "config.example.yaml contains an unexpected sensitive setting. Refusing to package it."
    }
    for ($index = 0; $index -lt $expectedSensitiveLines.Count; $index++) {
        if ($sensitiveLines[$index] -cne $expectedSensitiveLines[$index]) {
            throw "config.example.yaml contains an unexpected sensitive setting: $($sensitiveLines[$index])"
        }
    }
}

function Assert-NoPrivatePackageContent {
    param([string]$StagePath)
    foreach ($item in Get-ChildItem -LiteralPath $StagePath -Recurse -Force) {
        $relative = $item.FullName.Substring($StagePath.Length).TrimStart("\").Replace("\", "/")
        if ($relative -match "(?i)(^|/)(config\.yaml|data|logs|node_modules|\.git|\.venv|keys|\.env(?:\..*)?)(/|$)") {
            throw "Forbidden private/runtime content entered the package: $relative"
        }
        if (-not $item.PSIsContainer -and $item.Name -match "(?i)(\.(db|sqlite|sqlite3|log|pem|pfx|p12|key|map|pyc)|-wal|-shm)$") {
            throw "Forbidden private/runtime file entered the package: $relative"
        }
    }
}

function Copy-WindowsRegisterEngine {
    param(
        [string]$SourceRoot,
        [string]$DestinationRoot
    )
    if (-not (Test-Path -LiteralPath $SourceRoot -PathType Container)) {
        throw "Windows register engine source is missing: $SourceRoot"
    }
    [System.IO.Directory]::CreateDirectory($DestinationRoot) | Out-Null
    $excludeDirNames = @(
        ".venv",
        "keys",
        "logs",
        "__pycache__",
        ".pytest_cache",
        ".git"
    )
    Get-ChildItem -LiteralPath $SourceRoot -Force | ForEach-Object {
        $name = $_.Name
        if ($_.PSIsContainer -and ($excludeDirNames -contains $name)) {
            return
        }
        if (-not $_.PSIsContainer -and $name -match "(?i)^(\.env)$") {
            return
        }
        $target = Join-Path $DestinationRoot $name
        if ($_.PSIsContainer) {
            Copy-Item -LiteralPath $_.FullName -Destination $target -Recurse -Force
            Get-ChildItem -LiteralPath $target -Recurse -Force -Directory -ErrorAction SilentlyContinue |
                Where-Object { $excludeDirNames -contains $_.Name } |
                ForEach-Object { Remove-Item -LiteralPath $_.FullName -Recurse -Force -ErrorAction SilentlyContinue }
            Get-ChildItem -LiteralPath $target -Recurse -Force -File -ErrorAction SilentlyContinue |
                Where-Object { $_.Extension -match "(?i)^\.(pyc|pyo)$" -or $_.Name -eq ".env" } |
                ForEach-Object { Remove-Item -LiteralPath $_.FullName -Force -ErrorAction SilentlyContinue }
        }
        else {
            Copy-Item -LiteralPath $_.FullName -Destination $target -Force
        }
    }
    $required = @(
        (Join-Path $DestinationRoot "grok_register\register.py"),
        (Join-Path $DestinationRoot "requirements.txt"),
        (Join-Path $DestinationRoot "setup.ps1")
    )
    foreach ($path in $required) {
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "Packaged Windows register engine is incomplete; missing $path"
        }
    }
}

function Assert-WorkChildPath {
    param([string]$Path)
    $full = [System.IO.Path]::GetFullPath($Path)
    $prefix = [System.IO.Path]::GetFullPath($WorkRoot).TrimEnd("\") + "\"
    if (-not $full.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to modify a path outside the package staging directory: $full"
    }
    Assert-NoReparsePath $full $ProjectRoot
    return $full
}

function Reset-StagingDirectory {
    param([string]$Path)
    $full = Assert-WorkChildPath $Path
    if (Test-Path -LiteralPath $full) {
        Assert-NoReparseTree $full
        Remove-Item -LiteralPath $full -Recurse -Force
    }
    [System.IO.Directory]::CreateDirectory($full) | Out-Null
}

function Write-PackageChecksums {
    param([string]$StagePath)
    $lines = New-Object System.Collections.Generic.List[string]
    $manifestPath = [System.IO.Path]::GetFullPath((Join-Path $StagePath "SHA256SUMS.txt"))
    foreach ($file in Get-ChildItem -LiteralPath $StagePath -Recurse -File | Sort-Object FullName) {
        if ($file.FullName.Equals($manifestPath, [StringComparison]::OrdinalIgnoreCase)) {
            continue
        }
        $relative = $file.FullName.Substring($StagePath.Length).TrimStart("\").Replace("\", "/")
        $lines.Add(("{0}  {1}" -f (Get-FileSha256 $file.FullName), $relative))
    }
    [System.IO.File]::WriteAllLines((Join-Path $StagePath "SHA256SUMS.txt"), $lines, (New-Object Text.UTF8Encoding($false)))
}

function New-WindowsPackage {
    param(
        [string]$TargetArchitecture,
        [string]$GoPath,
        [string]$Version,
        [string]$SafeVersion,
        [string]$NodeVersion,
        [string]$PnpmVersion
    )
    $packageName = "grok2api-$SafeVersion-windows-$TargetArchitecture"
    $stagePath = Join-Path $WorkRoot $packageName
    Reset-StagingDirectory $stagePath

    $executableOutput = Join-Path $stagePath "grok2api.exe"
    $ldflags = "-s -w -X github.com/chenyme/grok2api/backend/internal/buildinfo.Version=$Version"
    Write-Step "Building backend for windows/$TargetArchitecture..."
    $oldCgo = $env:CGO_ENABLED
    $oldGoos = $env:GOOS
    $oldGoarch = $env:GOARCH
    $oldGoamd64 = $env:GOAMD64
    $oldGoarm64 = $env:GOARM64
    $oldGoflags = $env:GOFLAGS
    try {
        $env:CGO_ENABLED = "0"
        $env:GOOS = "windows"
        $env:GOARCH = $TargetArchitecture
        $env:GOFLAGS = ""
        if ($TargetArchitecture -eq "amd64") {
            $env:GOAMD64 = "v1"
            $env:GOARM64 = $null
        }
        else {
            $env:GOAMD64 = $null
            $env:GOARM64 = "v8.0"
        }
        Invoke-Checked $GoPath @(
            "build",
            "-buildvcs=false",
            "-trimpath",
            "-tags=timetzdata",
            "-ldflags=$ldflags",
            "-o", $executableOutput,
            "./cmd/grok2api"
        ) $BackendRoot
    }
    finally {
        $env:CGO_ENABLED = $oldCgo
        $env:GOOS = $oldGoos
        $env:GOARCH = $oldGoarch
        $env:GOAMD64 = $oldGoamd64
        $env:GOARM64 = $oldGoarm64
        $env:GOFLAGS = $oldGoflags
    }

    if (-not (Test-Path -LiteralPath $executableOutput -PathType Leaf)) {
        throw "Backend executable was not created for windows/$TargetArchitecture."
    }

    $frontendParent = Join-Path $stagePath "frontend"
    [System.IO.Directory]::CreateDirectory($frontendParent) | Out-Null
    Copy-Item -LiteralPath $FrontendDist -Destination $frontendParent -Recurse -Force
    foreach ($file in @("config.example.yaml", "VERSION", "LICENSE", "deploy.bat")) {
        Copy-Item -LiteralPath (Join-Path $ProjectRoot $file) -Destination (Join-Path $stagePath $file) -Force
    }
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot "deploy.ps1") -Destination (Join-Path $stagePath "deploy.ps1") -Force
    $deploymentDocPath = Join-Path $stagePath "DEPLOYMENT.md"
    Copy-Item -LiteralPath (Join-Path $ProjectRoot "WINDOWS_DEPLOYMENT.md") -Destination $deploymentDocPath -Force
    $deploymentDocContent = [System.IO.File]::ReadAllText($deploymentDocPath, [Text.Encoding]::UTF8)
    [System.IO.File]::WriteAllText($deploymentDocPath, $deploymentDocContent, (New-Object Text.UTF8Encoding($true)))
    [System.IO.File]::WriteAllText((Join-Path $stagePath "PACKAGE_PLATFORM"), "windows/$TargetArchitecture`r`n", [Text.Encoding]::ASCII)

    $registerSource = Join-Path $ProjectRoot "tools\windows-register"
    $registerDestination = Join-Path $stagePath "tools\windows-register"
    Write-Step "Packaging Windows register engine..."
    Copy-WindowsRegisterEngine -SourceRoot $registerSource -DestinationRoot $registerDestination

    $buildInfo = @(
        "Grok2API: $Version",
        "Platform: windows/$TargetArchitecture",
        "CGO: disabled",
        "Timezone database: embedded (timetzdata)",
        "Go: $(Get-GoVersion $GoPath)",
        "Node.js: $NodeVersion",
        "pnpm: $PnpmVersion",
        "Windows register engine: tools/windows-register (Python runtime not bundled)"
    )
    [System.IO.File]::WriteAllLines((Join-Path $stagePath "BUILDINFO.txt"), $buildInfo, [Text.Encoding]::ASCII)
    Write-PackageChecksums $stagePath

    if (-not (Test-Path -LiteralPath (Join-Path $stagePath "frontend\dist\index.html") -PathType Leaf)) {
        throw "Packaged frontend is missing index.html."
    }
    Assert-NoPrivatePackageContent $stagePath

    $zipPath = Join-Path $ArtifactWorkRoot ($packageName + ".zip")
    Write-Step "Compressing $packageName.zip..."
    Compress-Archive -LiteralPath $stagePath -DestinationPath $zipPath -CompressionLevel Optimal
    $safeStagePath = Assert-WorkChildPath $stagePath
    Assert-NoReparseTree $safeStagePath
    Remove-Item -LiteralPath $safeStagePath -Recurse -Force
    Write-Success "Staged $zipPath"
    return $zipPath
}

function Publish-Artifacts {
    param(
        [System.Collections.Generic.List[string]]$Created,
        [string]$ChecksumSource,
        [string]$SafeVersion
    )
    Assert-NoReparsePath $ReleaseRoot $ProjectRoot
    $published = New-Object System.Collections.Generic.List[string]
    foreach ($source in $Created) {
        $destination = Join-Path $ReleaseRoot (Split-Path -Leaf $source)
        $incoming = $destination + ".incoming"
        $backup = $destination + ".previous"
        if (Test-Path -LiteralPath $incoming) {
            Assert-NoReparsePath $incoming $ProjectRoot
            Remove-Item -LiteralPath $incoming -Force
        }
        if (Test-Path -LiteralPath $backup) {
            Assert-NoReparsePath $backup $ProjectRoot
            Remove-Item -LiteralPath $backup -Force
        }
        Move-Item -LiteralPath $source -Destination $incoming
        if (Test-Path -LiteralPath $destination -PathType Leaf) {
            Assert-NoReparsePath $destination $ProjectRoot
            [System.IO.File]::Replace($incoming, $destination, $backup)
            Remove-Item -LiteralPath $backup -Force
        }
        else {
            Move-Item -LiteralPath $incoming -Destination $destination
        }
        $published.Add($destination)
    }

    $publishedNames = @{}
    foreach ($path in $published) {
        $publishedNames[(Split-Path -Leaf $path).ToLowerInvariant()] = $true
    }
    foreach ($oldZip in Get-ChildItem -LiteralPath $ReleaseRoot -File -Filter "grok2api-$SafeVersion-windows-*.zip") {
        if (-not $publishedNames.ContainsKey($oldZip.Name.ToLowerInvariant())) {
            Remove-Item -LiteralPath $oldZip.FullName -Force
        }
    }

    $checksumDestination = Join-Path $ReleaseRoot "SHA256SUMS.txt"
    $checksumIncoming = $checksumDestination + ".incoming"
    $checksumBackup = $checksumDestination + ".previous"
    if (Test-Path -LiteralPath $checksumIncoming) {
        Assert-NoReparsePath $checksumIncoming $ProjectRoot
        Remove-Item -LiteralPath $checksumIncoming -Force
    }
    if (Test-Path -LiteralPath $checksumBackup) {
        Assert-NoReparsePath $checksumBackup $ProjectRoot
        Remove-Item -LiteralPath $checksumBackup -Force
    }
    Move-Item -LiteralPath $ChecksumSource -Destination $checksumIncoming
    if (Test-Path -LiteralPath $checksumDestination -PathType Leaf) {
        Assert-NoReparsePath $checksumDestination $ProjectRoot
        [System.IO.File]::Replace($checksumIncoming, $checksumDestination, $checksumBackup)
        Remove-Item -LiteralPath $checksumBackup -Force
    }
    else {
        Move-Item -LiteralPath $checksumIncoming -Destination $checksumDestination
    }
    return [pscustomobject]@{ Packages = $published; Checksums = $checksumDestination }
}

try {
    if ($PSVersionTable.PSVersion -lt [version]"5.1") {
        throw "Windows PowerShell 5.1 or later is required."
    }
    $lockDirectory = Join-Path $ProjectRoot ".tmp"
    Assert-NoReparsePath $lockDirectory $ProjectRoot
    [System.IO.Directory]::CreateDirectory($lockDirectory) | Out-Null
    $lockPath = Join-Path $lockDirectory "grok2api-windows-package.lock"
    try {
        $BuildLockStream = [System.IO.File]::Open(
            $lockPath,
            [System.IO.FileMode]::OpenOrCreate,
            [System.IO.FileAccess]::ReadWrite,
            [System.IO.FileShare]::None
        )
    }
    catch {
        throw "Another package.bat process is already using this workspace. Wait for it to finish and retry."
    }
    foreach ($required in @(
        (Join-Path $ProjectRoot "backend\go.mod"),
        (Join-Path $ProjectRoot "frontend\pnpm-lock.yaml"),
        (Join-Path $ProjectRoot "config.example.yaml"),
        (Join-Path $ProjectRoot "deploy.bat"),
        (Join-Path $ProjectRoot "WINDOWS_DEPLOYMENT.md")
    )) {
        if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
            throw "Missing project file: $required"
        }
    }
    Assert-ConfigTemplateSafe

    $version = ([System.IO.File]::ReadAllText((Join-Path $ProjectRoot "VERSION"))).Trim()
    if ($version -notmatch "^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$") {
        throw "VERSION contains characters that are unsafe for a release build: $version"
    }
    $safeVersion = $version.TrimStart("v")
    $hostArchitecture = Get-HostArchitecture
    [System.IO.Directory]::CreateDirectory($ToolsRoot) | Out-Null
    [System.IO.Directory]::CreateDirectory($DownloadsRoot) | Out-Null
    [System.IO.Directory]::CreateDirectory($WorkRoot) | Out-Null
    [System.IO.Directory]::CreateDirectory($ReleaseRoot) | Out-Null
    Assert-NoReparsePath $ToolsRoot $ProjectRoot
    Assert-NoReparsePath $WorkRoot $ProjectRoot
    Assert-NoReparsePath $ReleaseRoot $ProjectRoot
    Reset-StagingDirectory $ArtifactWorkRoot

    Write-Step "Checking build environment..."
    $goPath = Resolve-Go $hostArchitecture
    $nodePath = Resolve-Node $hostArchitecture
    $env:Path = (Split-Path -Parent $nodePath) + ";" + $env:Path
    $pnpmPath = Resolve-Pnpm $nodePath
    $goVersion = Get-GoVersion $goPath
    $nodeVersion = Get-NodeVersion $nodePath
    $pnpmVersion = Get-PnpmVersion $pnpmPath
    Write-Success "Build environment ready: Go $goVersion, Node.js $nodeVersion, pnpm $pnpmVersion"

    Write-Step "Installing locked frontend dependencies..."
    Invoke-Checked $pnpmPath @("install", "--frozen-lockfile", "--store-dir", $PnpmStore) $FrontendRoot

    if (-not $SkipChecks) {
        Write-Step "Running frontend lint..."
        Invoke-Checked $pnpmPath @("lint") $FrontendRoot
        Write-Step "Running backend tests..."
        Invoke-Checked $goPath @("test", "./...") $BackendRoot
        Write-Step "Running backend vet..."
        Invoke-Checked $goPath @("vet", "./...") $BackendRoot
    }
    else {
        Write-Host "[WARNING] Verification checks were skipped by request." -ForegroundColor Yellow
    }

    Write-Step "Building frontend..."
    Invoke-Checked $pnpmPath @("build") $FrontendRoot
    if (-not (Test-Path -LiteralPath (Join-Path $FrontendDist "index.html") -PathType Leaf)) {
        throw "Frontend build did not create dist/index.html."
    }

    $targets = if ($Architecture -eq "all") { @("amd64", "arm64") } else { @($Architecture) }
    $created = New-Object System.Collections.Generic.List[string]
    foreach ($target in $targets) {
        $zip = New-WindowsPackage $target $goPath $version $safeVersion $nodeVersion $pnpmVersion
        $created.Add($zip)
    }

    $checksumLines = New-Object System.Collections.Generic.List[string]
    foreach ($zip in $created) {
        $checksumLines.Add(("{0}  {1}" -f (Get-FileSha256 $zip), (Split-Path -Leaf $zip)))
    }
    $checksumSource = Join-Path $ArtifactWorkRoot "SHA256SUMS.txt"
    [System.IO.File]::WriteAllLines($checksumSource, $checksumLines, (New-Object Text.UTF8Encoding($false)))
    $published = Publish-Artifacts $created $checksumSource $safeVersion

    Write-Host ""
    Write-Success "Packaging complete. Upload the matching ZIP from: $ReleaseRoot"
    foreach ($zip in $published.Packages) {
        Write-Host ("  " + $zip)
    }
    Write-Host ("  " + $published.Checksums)
}
catch {
    Write-Host ("[ERROR] " + $_.Exception.Message) -ForegroundColor Red
    $PackageFailed = $true
}
finally {
    if ($null -ne $BuildLockStream) {
        $BuildLockStream.Dispose()
    }
}
if ($PackageFailed) {
    exit 1
}
