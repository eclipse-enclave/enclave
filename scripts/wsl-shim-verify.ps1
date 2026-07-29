# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

<#
.SYNOPSIS
    Checks that the Windows launcher's argument quoting survives a real wsl.exe.

.DESCRIPTION
    The launcher builds the Windows command line itself and hands it to
    CreateProcess unchanged. Its unit tests verify that against a
    reimplementation of CommandLineToArgvW, which is only as good as the
    assumption that wsl.exe parses the same way. This script tests the
    assumption against the real thing.

    It reads internal/wslshim/testdata/wsl-quoting-golden.json, which the Go
    tests generate from the launcher's own escaping code, so no quoting logic is
    duplicated here. Each command line is passed to wsl.exe verbatim and the
    Linux side echoes its argv NUL-separated: NUL is the one byte an argument
    cannot contain, so the comparison stays unambiguous even for arguments
    holding spaces, quotes, or newlines.

    This cannot run in GitHub-hosted CI, because the windows-latest runners have
    no WSL2. It is a manual pre-release gate for the Windows artifacts.

.PARAMETER Distro
    Distribution to test against. Defaults to the WSL default distribution.

.PARAMETER GoldenFile
    Path to the generated golden file. Defaults to the checked-in one.

.EXAMPLE
    pwsh -File scripts/wsl-shim-verify.ps1

.EXAMPLE
    pwsh -File scripts/wsl-shim-verify.ps1 -Distro Ubuntu-24.04
#>

[CmdletBinding()]
param(
    [string]$Distro = '',
    [string]$GoldenFile = (Join-Path $PSScriptRoot '..\internal\wslshim\testdata\wsl-quoting-golden.json')
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ($Distro -match '[\s"]') {
    throw "The -Distro name must not contain whitespace or quotes: '$Distro'"
}

# printf reuses its format for every remaining operand, so this writes each
# argument followed by a NUL. /usr/bin/printf is used explicitly rather than a
# shell builtin, because -e must not involve a shell at all.
$prefix = @()
if ($Distro) { $prefix += "-d $Distro" }
$prefix += '-e /usr/bin/printf %s\0'
$prefixArguments = $prefix -join ' '

function Invoke-Wsl {
    param([string]$Arguments)

    $psi = [System.Diagnostics.ProcessStartInfo]::new()
    $psi.FileName = 'wsl.exe'
    # Arguments is passed through verbatim, which is the point: the string under
    # test is the launcher's, not one .NET re-escaped.
    $psi.Arguments = $Arguments
    $psi.UseShellExecute = $false
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $psi.StandardOutputEncoding = [System.Text.UTF8Encoding]::new($false)
    $psi.StandardErrorEncoding = [System.Text.UTF8Encoding]::new($false)

    $process = [System.Diagnostics.Process]::Start($psi)
    $stdout = $process.StandardOutput.ReadToEnd()
    $stderr = $process.StandardError.ReadToEnd()
    $process.WaitForExit()

    return [pscustomobject]@{
        ExitCode = $process.ExitCode
        Stdout   = $stdout
        Stderr   = $stderr
    }
}

function Format-Label {
    param([string[]]$Values)

    $label = ($Values | ForEach-Object { "[$_]" }) -join ' '
    $label = $label -replace "`r", '\r' -replace "`n", '\n' -replace "`t", '\t'
    if ($label.Length -gt 72) { return $label.Substring(0, 69) + '...' }
    return $label
}

if (-not (Test-Path -LiteralPath $GoldenFile)) {
    throw "Golden file not found: $GoldenFile. Generate it with " +
          "ENCLAVE_UPDATE_GOLDEN=1 go test ./internal/wslshim"
}
$golden = Get-Content -LiteralPath $GoldenFile -Raw -Encoding utf8 | ConvertFrom-Json

Write-Host 'Checking that wsl.exe can run /usr/bin/printf...'
$probe = Invoke-Wsl "$prefixArguments probe"
if ($probe.ExitCode -ne 0) {
    throw "wsl.exe could not run /usr/bin/printf (exit $($probe.ExitCode)): $($probe.Stderr)"
}
if ($probe.Stdout -ne "probe`0") {
    throw "Unexpected probe output: $($probe.Stdout | ConvertTo-Json)"
}

$failures = @()
foreach ($case in $golden.cases) {
    $expected = @($case.args)
    $label = Format-Label $expected

    $result = Invoke-Wsl "$prefixArguments $($case.commandLine)"
    if ($result.ExitCode -ne 0) {
        Write-Host "FAIL $($case.name): $label"
        Write-Host "     wsl.exe exited $($result.ExitCode): $($result.Stderr)"
        $failures += $case.name
        continue
    }

    # Every argument is NUL-terminated, so split and drop the trailing empty
    # element the final NUL produces.
    $received = @($result.Stdout -split "`0")
    if ($received.Count -ge 2) {
        $received = @($received[0..($received.Count - 2)])
    } else {
        # No NUL at all means printf produced nothing recognizable.
        $received = @()
    }

    $mismatch = $received.Count -ne $expected.Count
    if (-not $mismatch) {
        for ($i = 0; $i -lt $expected.Count; $i++) {
            if ($received[$i] -ne $expected[$i]) { $mismatch = $true; break }
        }
    }

    if ($mismatch) {
        Write-Host "FAIL $($case.name): $label"
        Write-Host "     command line $($case.commandLine | ConvertTo-Json -Compress)"
        Write-Host "     sent         $($expected | ConvertTo-Json -Compress)"
        Write-Host "     received     $($received | ConvertTo-Json -Compress)"
        $failures += $case.name
    } else {
        Write-Host "ok   $($case.name)"
    }
}

Write-Host ''
if ($failures.Count -gt 0) {
    throw "$($failures.Count) of $($golden.cases.Count) cases did not round-trip through wsl.exe: $($failures -join ', ')"
}
Write-Host "All $($golden.cases.Count) cases round-tripped through wsl.exe unchanged."
