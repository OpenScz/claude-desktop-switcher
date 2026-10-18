param(
    [ValidateSet("Validate", "Build", "Installer", "All")]
    [string]$Task = "Validate",

    [ValidateSet("amd64", "arm64")]
    [string]$Arch = "amd64",

    [string]$Version = "dev",

    [string]$InstallerVersion = "",

    [string]$GoPath = "",

    [string]$InnoSetupPath = "",

    [switch]$Race,

    [switch]$SkipLint,

    [switch]$SkipSecurity
)

$ErrorActionPreference = "Stop"
$repository = Split-Path -Parent $PSScriptRoot
$dist = Join-Path $repository "installer\dist"
$windowsOutput = Join-Path $dist "windows_$Arch"
$binary = Join-Path $windowsOutput "claude-desktop-swap.exe"
$installerScript = Join-Path $repository "installer\windows-claude-swap.iss"

function Resolve-Go {
    if ($GoPath) {
        return (Resolve-Path -LiteralPath $GoPath -ErrorAction Stop).Path
    }

    $command = Get-Command go -ErrorAction SilentlyContinue
    if ($command) {
        return $command.Source
    }

    $name = if ($env:OS -eq "Windows_NT") { "go.exe" } else { "go" }
    $candidates = @((Join-Path $HOME "scoop\apps\go\current\bin\$name"))
    if ($env:ProgramFiles) {
        $candidates += Join-Path $env:ProgramFiles "Go\bin\$name"
    }
    foreach ($candidate in $candidates) {
        if ($candidate -and (Test-Path -LiteralPath $candidate)) {
            return (Resolve-Path -LiteralPath $candidate).Path
        }
    }

    $cacheRoot = Join-Path $HOME ".cache"
    $cached = Get-ChildItem -LiteralPath $cacheRoot -Directory -Filter "go*" -ErrorAction SilentlyContinue |
        ForEach-Object { Join-Path $_.FullName "bin\$name" } |
        Where-Object { Test-Path -LiteralPath $_ } |
        Sort-Object -Descending |
        Select-Object -First 1
    if ($cached) {
        return (Resolve-Path -LiteralPath $cached).Path
    }
    throw "Go was not found. Install Go, add it to PATH, or pass -GoPath"
}

function Invoke-Checked {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Command,

        [string[]]$Arguments
    )

    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Command failed with exit code $LASTEXITCODE"
    }
}

function Invoke-Validation {
    Push-Location $repository
    try {
        $gofmtName = if ($env:OS -eq "Windows_NT") { "gofmt.exe" } else { "gofmt" }
        $gofmt = Join-Path (Split-Path -Parent $go) $gofmtName
        if (-not (Test-Path -LiteralPath $gofmt)) {
            $goRoot = & $go env GOROOT
            if ($LASTEXITCODE -ne 0) {
                throw "Could not locate gofmt"
            }
            $gofmt = Join-Path $goRoot "bin\$gofmtName"
        }
        $goFiles = Get-ChildItem -LiteralPath $repository -Filter "*.go" -File -Recurse |
            Where-Object {
                $_.FullName -notlike "$repository\.git\*" -and
                $_.FullName -notlike "$dist\*"
            }
        $unformatted = & $gofmt -l @($goFiles.FullName)
        if ($LASTEXITCODE -ne 0) {
            throw "gofmt failed with exit code $LASTEXITCODE"
        }
        if ($unformatted) {
            throw "Go files need formatting:`n$($unformatted -join "`n")"
        }

        Invoke-Checked -Command $go -Arguments @("mod", "tidy", "-diff")
        Invoke-Checked -Command $go -Arguments @("vet", "./...")

        $testArguments = @("test", "-count=1")
        if ($Race) {
            $testArguments += "-race"
        }
        $testArguments += "./..."
        Invoke-Checked -Command $go -Arguments $testArguments

        if (-not $SkipLint) {
            Invoke-Checked -Command $go -Arguments @("run", "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0", "run")
        }
        if (-not $SkipSecurity) {
            Invoke-Checked -Command $go -Arguments @("run", "golang.org/x/vuln/cmd/govulncheck@v1.6.0", "./...")
        }
    } finally {
        Pop-Location
    }
}

function Invoke-WindowsBuild {
    if ($env:OS -ne "Windows_NT") {
        throw "Windows binaries and installers must be built on Windows"
    }

    New-Item -ItemType Directory -Path $windowsOutput -Force | Out-Null
    $previousGoos = $env:GOOS
    $previousGoarch = $env:GOARCH
    $previousCgo = $env:CGO_ENABLED
    try {
        $env:GOOS = "windows"
        $env:GOARCH = $Arch
        $env:CGO_ENABLED = "0"
        $ldflags = "-s -w -H=windowsgui -X github.com/TinoA/claude-desktop-switcher/cmd.Version=$Version"
        Push-Location $repository
        try {
            Invoke-Checked -Command $go -Arguments @("build", "-trimpath", "-ldflags", $ldflags, "-o", $binary, ".")
        } finally {
            Pop-Location
        }
    } finally {
        $env:GOOS = $previousGoos
        $env:GOARCH = $previousGoarch
        $env:CGO_ENABLED = $previousCgo
    }

    & (Join-Path $PSScriptRoot "build-windows-launcher.ps1") -Arch $Arch -OutputDir $windowsOutput
    if ($LASTEXITCODE -ne 0) {
        throw "Native launcher build failed with exit code $LASTEXITCODE"
    }
}

function Resolve-InnoSetup {
    if ($InnoSetupPath) {
        $resolved = Resolve-Path -LiteralPath $InnoSetupPath -ErrorAction Stop
        return $resolved.Path
    }

    $candidates = @(
        (Join-Path $env:LOCALAPPDATA "Programs\Inno Setup 6\ISCC.exe"),
        (Join-Path ${env:ProgramFiles(x86)} "Inno Setup 6\ISCC.exe"),
        (Join-Path $env:ProgramFiles "Inno Setup 6\ISCC.exe"),
        (Join-Path $HOME ".cache\inno-setup-6.7.3\ISCC.exe")
    )
    foreach ($candidate in $candidates) {
        if ($candidate -and (Test-Path -LiteralPath $candidate)) {
            return (Resolve-Path -LiteralPath $candidate).Path
        }
    }

    $cached = Get-ChildItem -LiteralPath (Join-Path $HOME ".cache") -Directory -Filter "inno-setup-*" -ErrorAction SilentlyContinue |
        ForEach-Object { Join-Path $_.FullName "ISCC.exe" } |
        Where-Object { Test-Path -LiteralPath $_ } |
        Sort-Object -Descending |
        Select-Object -First 1
    if ($cached) {
        return (Resolve-Path -LiteralPath $cached).Path
    }
    throw "Inno Setup 6 was not found. Pass -InnoSetupPath with the full path to ISCC.exe"
}

function Invoke-InstallerBuild {
    $iscc = Resolve-InnoSetup
    $displayVersion = if ($InstallerVersion) { $InstallerVersion } else { $Version.TrimStart("v") }
    Push-Location (Split-Path -Parent $installerScript)
    try {
        Invoke-Checked -Command $iscc -Arguments @("/DAppArch=$Arch", "/DAppVersion=$displayVersion", $installerScript)
    } finally {
        Pop-Location
    }
}

$go = Resolve-Go

switch ($Task) {
    "Validate" {
        Invoke-Validation
    }
    "Build" {
        Invoke-WindowsBuild
    }
    "Installer" {
        Invoke-WindowsBuild
        Invoke-InstallerBuild
    }
    "All" {
        Invoke-Validation
        Invoke-WindowsBuild
        Invoke-InstallerBuild
    }
}
