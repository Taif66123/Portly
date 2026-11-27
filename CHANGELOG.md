# Changelog

## Unreleased

- Windows: `install.ps1` adds the install folder to the user `PATH`, keeping `%VARIABLE%` entries as they are, so `portly` runs in new terminals; `uninstall.ps1` removes it. It installs the 64-bit build also from 32-bit PowerShell, and the ARM64 build from emulated PowerShell on ARM64. It turns on TLS 1.2 where Windows PowerShell lacks it, and stops early on Windows older than 10. The README has an install line for Windows without `curl.exe`.

## 0.8.3 (2026-11-27)

- Same program as 0.8.2. The release check for the Windows cmd install command is fixed.

## 0.8.2 (2026-11-27)

- Stopping works on Linux (amd64 and arm64), for processes and Docker containers. Its evidence gate passed with real gunicorn, uvicorn, Django, Flask and Node servers. macOS, Windows and other Linux processors still refuse.
- `--force` asks for `kill` again also when a target has no graceful stop.
- Containers that a systemd service, Nomad or ECS would start again are refused.
- `--expect` and the last check before the signal also cover containers sharing the target's network and new child processes.
- Every release before 1.0.0 is published as a pre-release. Install it by its tag; `portly update` works from 1.0.0.

## 0.8.0 (2026-11-27)

First public release.

- List and inspect TCP and UDP ports with their process, container and project.
- In a terminal, the list is a bordered table that keeps the directory over the address when space is short, and long directories are shortened in the middle. Narrow terminals, `TERM=dumb` and piped output get plain columns; piped output keeps full paths.
- Stop by port with a plan, confirmation, revalidation and verification. Switched off until each platform's evidence gate has passed.
- Docker and Podman containers through their local engine. Podman containers are listed, not stopped.
- JSON output, exit statuses and reason codes, version 1.
- `portly update` replaces the binary with the latest verified release, after asking.
- Install, update and uninstall scripts for macOS, Linux and Windows. Packages for apt, dnf, zypper, apk and pacman.
