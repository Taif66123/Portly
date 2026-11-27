# Installs and removes Portly with this checkout's install.ps1 and uninstall.ps1, run the way the README runs them,
# and checks the user PATH. It changes the user's Path for the test and restores it exactly.
#   tests/windows-install.ps1 -Shell powershell -Arch amd64
param(
    [string]$Shell = 'powershell',
    [string]$Version = '0.8.3',
    [Parameter(Mandatory)][string]$Arch
)
$ErrorActionPreference = 'Stop'
$resources = Join-Path (Split-Path $PSScriptRoot) 'resources'
$directory = Join-Path ([IO.Path]::GetTempPath()) "portly install $([IO.Path]::GetRandomFileName())"
$seed = '%USERPROFILE%\portly-seed'
function Quote([string]$text) { "'" + ($text -replace "'", "''") + "'" }
function Check([bool]$ok, [string]$what) { if (-not $ok) { throw "FAILED: $what" }; Write-Output "ok: $what" }
function Read-UserPath {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment')
    try { [string]$key.GetValue('Path', '', 'DoNotExpandEnvironmentNames'), $key.GetValueKind('Path') } finally { $key.Close() }
}
function Invoke-Shell([string]$command) {
    $output = & $Shell -NoProfile -ExecutionPolicy Bypass -Command $command 2>&1 | Out-String
    Write-Output $output
    if ($LASTEXITCODE -ne 0) { throw "FAILED: $Shell exited with $LASTEXITCODE" }
}

$key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
$savedPath = $key.GetValue('Path', $null, 'DoNotExpandEnvironmentNames')
$savedKind = if ($null -ne $savedPath) { $key.GetValueKind('Path') }
$key.SetValue('Path', (@(@("$savedPath" -split ';' | Where-Object { $_ }) + $seed) -join ';'), 'ExpandString')
$key.Close()
$env:PORTLY_VERSION, $env:PORTLY_INSTALL_DIR = $Version, $directory
try {
    $output = Invoke-Shell "Get-Content $(Quote "$resources\install.ps1") | Out-String | Invoke-Expression; portly --version"
    Check ($output -match "portly $([regex]::Escape($Version))") 'portly runs in the installing session'
    Check ($output -match "\(windows/$Arch\)") "the windows/$Arch build was installed"
    $path, $kind = Read-UserPath
    Check ($kind -eq 'ExpandString') 'the user Path is still REG_EXPAND_SZ'
    Check (($path -split ';') -contains $seed) 'other %VARIABLE% entries stay unexpanded'
    Check (($path -split ';') -contains $directory) 'the install folder is on the user Path'

    $fresh = "`$env:Path = [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('Path', 'User'); portly --version"
    Check ((Invoke-Shell $fresh) -match "portly $([regex]::Escape($Version))") 'a new terminal finds portly'

    $output = Invoke-Shell "Get-Content $(Quote "$resources\install.ps1") | Out-String | Invoke-Expression"
    $path, $kind = Read-UserPath
    Check (@($path -split ';' | Where-Object { $_ -eq $directory }).Count -eq 1) 'installing again adds the folder once'

    Invoke-Shell "Get-Content $(Quote "$resources\uninstall.ps1") | Out-String | Invoke-Expression; if (Get-Command portly -ErrorAction SilentlyContinue) { exit 3 }" | Out-Null
    $path, $kind = Read-UserPath
    Check (-not (Test-Path (Join-Path $directory 'portly.exe'))) 'uninstall removed portly.exe'
    Check (($path -split ';') -notcontains $directory) 'uninstall removed the folder from the user Path'
    Check (($path -split ';') -contains $seed -and $kind -eq 'ExpandString') 'uninstall kept the other entries'
} finally {
    $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
    if ($null -eq $savedPath) { $key.DeleteValue('Path', $false) } else { $key.SetValue('Path', $savedPath, $savedKind) }
    $key.Close()
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $directory
    Remove-Item Env:PORTLY_VERSION, Env:PORTLY_INSTALL_DIR
}
