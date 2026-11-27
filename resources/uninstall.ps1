# Remove Portly installed by install.ps1, and its folder from the user PATH.
$ErrorActionPreference = 'Stop'
$directory = if ($env:PORTLY_INSTALL_DIR) { $env:PORTLY_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\Portly' }
$binary = Join-Path $directory 'portly.exe'
if (-not (Test-Path -PathType Leaf $binary)) { throw "portly is not installed in $directory" }
if ((& $binary --version) -notmatch '^portly ') { throw "$binary is not portly, so it was left alone" }
$directory = (Resolve-Path -LiteralPath $directory).ProviderPath.TrimEnd('\')
Remove-Item -Force $binary
Remove-Item -Force -ErrorAction SilentlyContinue "$binary.old*"
if (-not (Get-ChildItem -Force $directory)) { Remove-Item $directory }

# Read and write the registry value itself: going through [Environment] would expand %VARIABLES% in the user's Path for good
$key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
try {
    $path = [string]$key.GetValue('Path', '', 'DoNotExpandEnvironmentNames')
    $entries = @($path -split ';' | Where-Object { $_ })
    $kept = @($entries | Where-Object { [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') -ne $directory })
    if ($kept.Count -ne $entries.Count) {
        $key.SetValue('Path', ($kept -join ';'), $key.GetValueKind('Path'))
        # Removing a variable that does not exist changes nothing, and tells running programs to reload the environment
        [Environment]::SetEnvironmentVariable('PORTLY_PATH_REFRESH', $null, 'User')
    }
} finally {
    $key.Close()
}
$env:Path = ($env:Path -split ';' | Where-Object { $_ -and $_.TrimEnd('\') -ne $directory }) -join ';'
Write-Output "Removed portly from $directory"
