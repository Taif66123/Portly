# Portly

See what uses your local ports, which project it belongs to, and stop it safely. One binary, no configuration.

Running `portly` prints a table like this one:

| PORT | PROTO | PROJECT | TYPE | PID | PROCESS | ADDRESS | DIRECTORY |
|---|---|---|---|---|---|---|---|
| 22 | tcp | Unknown | Native | — | Permission denied | 0.0.0.0 | — |
| 5432 | tcp | shop | Docker | — | shop-db-1 | 127.0.0.1 | ~/shop |
| 8000 | tcp | shop | Python | 6182 | uvicorn | 127.0.0.1 | ~/shop |

In a terminal the table has borders, and columns that don't fit are dropped from the right. Long directories are shortened in the middle, like `~/…/acme/web-app`. Piped output has plain columns and full paths.

| Command | Does |
|---|---|
| `portly` | List listening TCP and bound UDP ports |
| `portly --all` | Also list likely-client UDP ports |
| `portly 8000` | Details for a port, or several |
| `portly stop 8000` | Show exactly what would stop, ask, stop it gracefully |
| `portly stop 5353/udp` | Stop a UDP owner. A bare port means TCP |
| `portly stop 8000 --yes` | Do not ask. Every check still runs |
| `portly stop 8000 --force` | Graceful first when possible. Asks again before any kill |
| `portly update` | Update to the latest release, after asking (from 1.0.0, see Update below) |
| `--json` | One JSON document, for scripts and agents |

## Status

Listing and lookup work on Linux. macOS and Windows are built and unit tested, and not yet confirmed on real machines.

**Stopping works on Linux (amd64 and arm64), for processes and Docker containers.** It is still switched off on macOS and Windows. Each platform has an evidence gate, a set of tests against real servers (gunicorn, uvicorn, Django, Flask, Node), and stopping is enabled only after its gate has passed there. Until then `portly stop` explains that and stops nothing. Podman containers are listed, never stopped. Version 0.8.0 had every gate closed.

## Install

Every release is on the [releases page](https://github.com/AnindyaMajumder/Portly/releases), with checksums signed by Sigstore. Until 1.0.0 every release is a pre-release, so the commands below name the version.

**macOS, Linux, and Windows in Git Bash or WSL**

```
curl -fsSL https://github.com/AnindyaMajumder/Portly/releases/download/v0.8.3/install.sh | PORTLY_VERSION=0.8.3 sh
```

**Windows PowerShell**

```
$env:PORTLY_VERSION='0.8.3'; curl.exe -fsSL https://github.com/AnindyaMajumder/Portly/releases/download/v0.8.3/install.ps1 | Out-String | iex
```

**Windows cmd**

```
powershell -NoProfile -Command "$env:PORTLY_VERSION='0.8.3'; curl.exe -fsSL https://github.com/AnindyaMajumder/Portly/releases/download/v0.8.3/install.ps1 | Out-String | iex"
```

**Windows without `curl.exe`** (Windows Server 2016, Windows 10 before 1803)

```
[Net.ServicePointManager]::SecurityProtocol = 'Tls12'; $env:PORTLY_VERSION='0.8.3'; iex (New-Object Net.WebClient).DownloadString('https://github.com/AnindyaMajumder/Portly/releases/download/v0.8.3/install.ps1')
```

All of them install for the current user, without administrator rights, and verify the checksum. `install.sh` puts `portly` in `~/.local/bin` and prints the command to add that folder to your `PATH` if needed. `install.ps1` puts `portly.exe` in `%LOCALAPPDATA%\Programs\Portly` and adds that folder to your user `PATH`, so `portly` works in that PowerShell at once and in terminals opened afterwards (restart VS Code and similar apps first); `uninstall.ps1` takes it out again. It picks the build for the processor of Windows itself, also from 32-bit or emulated PowerShell, and needs Windows 10, Windows Server 2016 or later. In WSL you get the Linux build, which sees the ports of the WSL machine, not of Windows.

Variables go after the pipe, so `sh` sees them. To choose the folder:

```
curl -fsSL https://github.com/AnindyaMajumder/Portly/releases/download/v0.8.3/install.sh | PORTLY_VERSION=0.8.3 PORTLY_INSTALL_DIR=/opt/bin sh
```

### Update

Until 1.0.0, update by running the install command of the newer version from the [releases page](https://github.com/AnindyaMajumder/Portly/releases). It replaces the old binary.

`portly update` only sees releases that are not pre-releases, so it starts working with 1.0.0. Until then it answers "No release has been published yet" and changes nothing.

### Uninstall

macOS, Linux, Git Bash:

```
curl -fsSL https://github.com/AnindyaMajumder/Portly/releases/download/v0.8.3/uninstall.sh | sh
```

Windows PowerShell:

```
curl.exe -fsSL https://github.com/AnindyaMajumder/Portly/releases/download/v0.8.3/uninstall.ps1 | Out-String | iex
```

Windows cmd:

```
powershell -NoProfile -Command "curl.exe -fsSL https://github.com/AnindyaMajumder/Portly/releases/download/v0.8.3/uninstall.ps1 | Out-String | iex"
```

### Linux packages

Download the package for your system from the releases page (`amd64`, `arm64`, `386`, `armv6`, `armv7` and more), then:

| System | Install | Remove |
|---|---|---|
| Debian, Ubuntu (apt) | `sudo apt install ./portly_0.8.3_linux_amd64.deb` | `sudo apt remove portly` |
| Fedora, RHEL (dnf) | `sudo dnf install ./portly_0.8.3_linux_amd64.rpm` | `sudo dnf remove portly` |
| openSUSE (zypper) | `sudo zypper install --allow-unsigned-rpm ./portly_0.8.3_linux_amd64.rpm` | `sudo zypper remove portly` |
| Alpine (apk) | `sudo apk add --allow-untrusted ./portly_0.8.3_linux_amd64.apk` | `sudo apk del portly` |
| Arch (pacman) | `sudo pacman -U ./portly_0.8.3_linux_amd64.pkg.tar.zst` | `sudo pacman -R portly` |

Packages include the man page and shell completions. To update, install the newer package the same way.

### Archives

`.tar.gz` for Linux and macOS, `.zip` for Windows. Each holds one binary: put it anywhere on your `PATH`. A macOS browser download is quarantined: run `xattr -d com.apple.quarantine portly` once, or use the curl command above.

### From source

With Go 1.27.4 or later: `make build`, then `bin/portly`.

## Safety

Portly refuses rather than guesses. It never stops a port with several owners, a system or supervised process, a port forwarder, or a container it can't prove is local. It never runs sudo and never reads other processes' environment variables. Secrets in command lines are masked, best effort. A kill happens only with `--force`, after a graceful stop was delivered and failed or when none exists, and only after you type `kill` (or pass `--yes`). Containers restarted by systemd, Nomad, ECS, Swarm or Kubernetes are refused.

## For scripts and agents

```
portly stop 8000 --json                          # stops nothing, returns a plan_id
portly stop 8000 --yes --json --expect <plan_id> # stops only those same targets
```

Output, reason codes and exit statuses are described in [resources/json.md](resources/json.md). The design is in [resources/ARCHITECTURE.md](resources/ARCHITECTURE.md).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security problems: [SECURITY.md](SECURITY.md).

## License

MIT
