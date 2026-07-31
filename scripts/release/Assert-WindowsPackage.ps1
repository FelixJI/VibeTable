[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Path,

    [long]$MaximumExpandedBytes = 536870912
)

$ErrorActionPreference = "Stop"

function Add-Failure {
    param([string]$Message)
    $script:Failures.Add($Message)
}

function Test-ReleaseRoot {
    param(
        [System.IO.DirectoryInfo]$Root,
        [string]$PackageName,
        [string]$ArchiveName
    )

    if ($ArchiveName) {
        if ($PackageName -cne "VibeTable") {
            Add-Failure "Archive top-level directory must be exactly 'VibeTable'; found '$PackageName'."
        }
        if ($ArchiveName -notmatch '^VibeTable-v\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?-win-x64\.zip$') {
            Add-Failure "Archive '$ArchiveName' does not include a semantic version and win-x64 suffix."
        }
    }

    $entries = @(Get-ChildItem -LiteralPath $Root.FullName -Force)
    $files = @($entries | Where-Object { -not $_.PSIsContainer })
    $directories = @($entries | Where-Object { $_.PSIsContainer })
    $fileNames = @($files.Name)
    $directoryNames = @($directories.Name)

    if ("VibeTable.Next.exe" -notin $fileNames) {
        Add-Failure "VibeTable.Next.exe is missing from the package root."
    }

    if ("resources" -notin $directoryNames) {
        Add-Failure "The package root must contain one resources directory for internal application files."
    }

    $forbiddenDirectories = @(
        "userdata",
        "VibeTableData",
        "backend", "contracts", "sidecar", "web-grid", "runtimes",
        "cs", "de", "es", "fr", "it", "ja", "ko", "pl", "pt-BR", "ru", "tr",
        "zh-Hans", "zh-Hant"
    )
    foreach ($name in $forbiddenDirectories) {
        if ($name -in $directoryNames) {
            Add-Failure "Forbidden package-root directory found: $name"
        }
    }

    $looseImplementationFiles = @(
        $files | Where-Object {
            $_.Extension -in @(".dll", ".pdb", ".xml", ".json") -and
            $_.Name -ne "release.json"
        }
    )
    if ($looseImplementationFiles.Count -gt 0) {
        Add-Failure (
            "Loose implementation files found in package root: " +
            (($looseImplementationFiles.Name | Select-Object -First 8) -join ", ")
        )
    }

    $allFiles = @(Get-ChildItem -LiteralPath $Root.FullName -File -Recurse -Force)
    $expandedBytes = ($allFiles | Measure-Object -Property Length -Sum).Sum
    if ($null -eq $expandedBytes) {
        $expandedBytes = 0
    }
    if ($expandedBytes -gt $MaximumExpandedBytes) {
        Add-Failure "Expanded package is $expandedBytes bytes; limit is $MaximumExpandedBytes bytes."
    }

    $releaseManifest = Join-Path $Root.FullName "release.json"
    if (-not (Test-Path -LiteralPath $releaseManifest -PathType Leaf)) {
        Add-Failure "release.json is missing from the package root."
    }
    else {
        try {
            $manifest = Get-Content -LiteralPath $releaseManifest -Raw | ConvertFrom-Json
            if ([string]::IsNullOrWhiteSpace([string]$manifest.version)) {
                Add-Failure "release.json does not contain a version."
            }
            elseif ($ArchiveName -and $ArchiveName -ne "VibeTable-v$($manifest.version)-win-x64.zip") {
                Add-Failure "release.json version '$($manifest.version)' does not match archive '$ArchiveName'."
            }
        }
        catch {
            Add-Failure "release.json is not valid JSON: $($_.Exception.Message)"
        }
    }

    $executable = Join-Path $Root.FullName "VibeTable.Next.exe"
    if (Test-Path -LiteralPath $executable -PathType Leaf) {
        $versionInfo = [System.Diagnostics.FileVersionInfo]::GetVersionInfo($executable)
        if ([string]::IsNullOrWhiteSpace($versionInfo.FileVersion)) {
            Add-Failure "VibeTable.Next.exe has no file version."
        }

        Add-Type -AssemblyName System.Drawing
        $icon = [System.Drawing.Icon]::ExtractAssociatedIcon($executable)
        if ($null -eq $icon) {
            Add-Failure "VibeTable.Next.exe has no associated Windows icon."
        }
        else {
            $icon.Dispose()
        }
    }

    [pscustomobject]@{
        PackageRoot = $Root.FullName
        FileCount = $allFiles.Count
        ExpandedBytes = [long]$expandedBytes
    }
}

$Failures = [System.Collections.Generic.List[string]]::new()
$resolved = Get-Item -LiteralPath $Path
$temporaryRoot = $null

try {
    if ($resolved.PSIsContainer) {
        $packageRoot = $resolved
        $packageName = $resolved.Name
        $archiveName = ""
    }
    elseif ($resolved.Extension -ieq ".zip") {
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        $archive = [System.IO.Compression.ZipFile]::OpenRead($resolved.FullName)
        try {
            $topLevels = @(
                $archive.Entries |
                    Where-Object { -not [string]::IsNullOrWhiteSpace($_.FullName) } |
                    ForEach-Object { ($_.FullName -replace '\\', '/').Split('/')[0] } |
                    Sort-Object -Unique
            )
        }
        finally {
            $archive.Dispose()
        }

        if ($topLevels.Count -ne 1) {
            Add-Failure "Archive must contain exactly one top-level directory; found $($topLevels.Count)."
        }
        elseif ($topLevels[0] -cne "VibeTable") {
            Add-Failure "Archive top-level directory must be exactly 'VibeTable'; found '$($topLevels[0])'."
        }

        $repositoryRoot = [System.IO.Path]::GetFullPath(
            (Join-Path $PSScriptRoot "..\..")
        )
        $packageChecksRoot = Join-Path $repositoryRoot "build\package-checks"
        [System.IO.Directory]::CreateDirectory($packageChecksRoot) | Out-Null
        $temporaryRoot = Join-Path $packageChecksRoot (
            "vibetable-release-check-" + [Guid]::NewGuid().ToString("N")
        )
        [System.IO.Compression.ZipFile]::ExtractToDirectory($resolved.FullName, $temporaryRoot)

        if ($topLevels.Count -eq 1) {
            $packageRoot = Get-Item -LiteralPath (Join-Path $temporaryRoot $topLevels[0])
            $packageName = $topLevels[0]
        }
        else {
            $packageRoot = Get-Item -LiteralPath $temporaryRoot
            $packageName = $resolved.BaseName
        }
        $archiveName = $resolved.Name
    }
    else {
        throw "Path must be a release directory or .zip archive."
    }

    $summary = Test-ReleaseRoot `
        -Root $packageRoot `
        -PackageName $packageName `
        -ArchiveName $archiveName
    $summary | Format-List | Out-String | Write-Host

    if ($Failures.Count -gt 0) {
        Write-Host "Release package validation failed:" -ForegroundColor Red
        foreach ($failure in $Failures) {
            Write-Host " - $failure" -ForegroundColor Red
        }
        exit 1
    }

    Write-Host "Release package validation passed." -ForegroundColor Green
}
finally {
    if ($null -ne $temporaryRoot -and (Test-Path -LiteralPath $temporaryRoot)) {
        Remove-Item -LiteralPath $temporaryRoot -Recurse -Force
    }
}
