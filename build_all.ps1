# Requires: PowerShell 5+, Go toolchain, git (optional for version)

# Colors
$Red = 'Red'
$Green = 'Green'
$White = 'White'

# OS/ARCH matrix
$osList = @('windows', 'linux', 'darwin', 'freebsd')
$archList = @('amd64', 'arm64', '386', 'arm')

# Ensure build directory
$buildDir = Join-Path -Path (Get-Location) -ChildPath 'build'
New-Item -ItemType Directory -Force -Path $buildDir | Out-Null

# Detect version from git tags or fallback to dev
$version = (git describe --tags --abbrev=0 2>$null)
if (-not $version) { $version = 'dev' }
$version = $version.Trim()

# Check go exists
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host 'Go toolchain not found in PATH. Please install Go and try again.' -ForegroundColor $Red
    exit 1
}

$failedBuilds = @()

foreach ($goos in $osList) {
    foreach ($goarch in $archList) {
        # Skip unsupported combos: windows/arm, darwin/386, darwin/arm
        if ((($goos -eq 'windows') -and ($goarch -eq 'arm')) -or
            (($goos -eq 'darwin') -and (($goarch -eq '386') -or ($goarch -eq 'arm')))) {
            continue
        }

        Write-Host "Building for $goos/$goarch..." -ForegroundColor $White

        $binaryName = "komari-agent-$goos-$goarch"
        if ($goos -eq 'windows') { $binaryName = "$binaryName.exe" }
        $outPath = Join-Path $buildDir $binaryName

        # Set env per invocation
        $env:GOOS = $goos
        $env:GOARCH = $goarch
        $env:CGO_ENABLED = '0'

        & go build -trimpath -ldflags "-X github.com/komari-monitor/komari-agent/update.CurrentVersion=$version" -o "$outPath"
        if ($LASTEXITCODE -ne 0) {
            Write-Host "Failed to build for $goos/$goarch" -ForegroundColor $Red
            $failedBuilds += "$goos/$goarch"
        }
        else {
            Write-Host "Successfully built $binaryName" -ForegroundColor $Green
        }

        # Clear env to avoid affecting subsequent shells (optional)
        Remove-Item Env:GOOS -ErrorAction SilentlyContinue
        Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
        Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
    }
}

if ($failedBuilds.Count -gt 0) {
    Write-Host "`nThe following builds failed:" -ForegroundColor $Red
    foreach ($b in $failedBuilds) { Write-Host "- $b" -ForegroundColor $Red }
}
else {
    Write-Host "`nAll builds completed successfully." -ForegroundColor $Green
}

Write-Host "`nBinaries are in the ./build directory." -ForegroundColor $White
