# Install Portly for the current user. Verifies the checksum and adds the folder to the user PATH.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$repository = 'https://github.com/AnindyaMajumder/Portly'
if ([Environment]::OSVersion.Version.Major -lt 10) { throw 'Portly needs Windows 10, Windows Server 2016 or later' }
# Older Windows PowerShell may not offer TLS 1.2, which GitHub requires
$protocols = [Net.ServicePointManager]::SecurityProtocol
if ($protocols -ne 0 -and -not ($protocols -band [Net.SecurityProtocolType]::Tls12)) {
    [Net.ServicePointManager]::SecurityProtocol = $protocols -bor [Net.SecurityProtocolType]::Tls12
}
$directory = if ($env:PORTLY_INSTALL_DIR) { $env:PORTLY_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\Portly' }
# The processor of Windows itself, also from 32-bit or emulated PowerShell
$native = (Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Environment' -ErrorAction SilentlyContinue).PROCESSOR_ARCHITECTURE
if (-not $native) { $native = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE } }
$arch = switch ($native) { 'AMD64' { 'amd64' } 'ARM64' { 'arm64' } 'x86' { '386' } default { throw "unsupported processor $_" } }

# The latest release, from GitHub's redirect like install.sh, which has no rate limit
$version = "$env:PORTLY_VERSION".TrimStart('v')
if (-not $version) {
    $request = [System.Net.WebRequest]::Create("$repository/releases/latest")
    $request.AllowAutoRedirect = $false
    $response = $request.GetResponse()
    $version = ($response.Headers['Location'] -split '/v')[-1]
    $response.Close()
}
if ($version -notmatch '^[0-9A-Za-z.-]+$') { throw "could not find the latest release, see $repository/releases" }

$temporary = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $temporary | Out-Null
try {
    $archive = "portly_${version}_windows_${arch}.zip"
    foreach ($file in $archive, 'checksums.txt') {
        try {
            Invoke-WebRequest -UseBasicParsing "$repository/releases/download/v$version/$file" -OutFile (Join-Path $temporary $file)
        } catch {
            throw "could not download $file. Check PORTLY_VERSION and your connection, or see $repository/releases"
        }
    }
    $line = Get-Content (Join-Path $temporary 'checksums.txt') | Where-Object { ($_ -split '\s+')[1] -eq $archive }
    $actual = (Get-FileHash -Algorithm SHA256 (Join-Path $temporary $archive)).Hash
    if (-not $line -or ($line -split '\s+')[0] -ne $actual) { throw 'checksum does not match, nothing was installed' }
    Expand-Archive (Join-Path $temporary $archive) $temporary
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
    $directory = (Resolve-Path -LiteralPath $directory).ProviderPath.TrimEnd('\')
    Move-Item -Force (Join-Path $temporary 'portly.exe') (Join-Path $directory 'portly.exe')
    Write-Output "Installed portly $version (windows/$arch) in $directory"
} finally {
    Remove-Item -Recurse -Force $temporary
}

# Read and write the registry value itself: going through [Environment] would expand %VARIABLES% in the user's Path for good
$key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
try {
    $path = [string]$key.GetValue('Path', '', 'DoNotExpandEnvironmentNames')
    $kind = if ($path) { $key.GetValueKind('Path') } else { [Microsoft.Win32.RegistryValueKind]::ExpandString }
    $entries = @($path -split ';' | Where-Object { $_ })
    $isListed = $entries | Where-Object { [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') -eq $directory }
    if (-not $isListed) {
        $key.SetValue('Path', (($entries + $directory) -join ';'), $kind)
        # Removing a variable that does not exist changes nothing, and tells running programs to reload the environment
        [Environment]::SetEnvironmentVariable('PORTLY_PATH_REFRESH', $null, 'User')
        Write-Output "Added $directory to your user PATH. Other terminals see it once reopened; restart VS Code and similar apps first."
    }
} finally {
    $key.Close()
}
if (-not ($env:Path -split ';' | Where-Object { $_.TrimEnd('\') -eq $directory })) { $env:Path = "$($env:Path.TrimEnd(';'));$directory" }
