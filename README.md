# NeoLauncher

A Windows-first Minecraft Java launcher with the NeoPanel look and lightweight local server setup.

## Current build

This is the first development scaffold, not a finished Minecraft launcher.

- Native Windows window with Play, Servers, and Settings.
- Saved Java path and memory preferences.
- Local server profiles with separate folders, `server.properties`, and `eula.txt`.
- No full NeoPanel administration, roles, billing, or remote-node management.

**Not implemented yet:** Microsoft sign-in, Minecraft downloads/installation, game launch, and server download/start/stop. The interface labels these honestly; creating a profile does not install or start Minecraft.

## Build and run

Requires Windows and Go 1.26+. From this folder:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\build-windows.ps1
.\dist\windows-amd64\NeoLauncher.exe
```

The EXE needs Microsoft Edge WebView2 Runtime (normally included with Windows). Go is needed only to build. Builds are currently unsigned. Use `-Architecture arm64` for an ARM64 cross-build.

Settings and profiles live in `%LOCALAPPDATA%\NeoLauncher`, separate from NeoPanel and your existing Minecraft files. For a test instance:

```powershell
.\dist\windows-amd64\NeoLauncher.exe -data-dir 'D:\NeoLauncher Test'
```

Server profiles leave `eula=false`. You must review and accept the Minecraft EULA before running a server. No firewall changes, elevation, downloads, or game processes happen automatically.

## Development

The UI is plain HTML/CSS/JavaScript embedded in a Go/Wails application. No Node bundler or Wails CLI is required.

```powershell
go test ./...
go vet ./...
node --test tests/*.test.cjs
```

Core settings/profile tests also run on Linux. The desktop window currently targets Windows only. Opening `internal/app/frontend/index.html` in a browser provides a clearly labeled layout preview; it does not write server files.

Next milestones:
1. Microsoft account sign-in and secure token storage.
2. Verified Minecraft version, library, and asset installation.
3. Authenticated game launch.
4. Minimal server download, explicit EULA acceptance, and safe start/stop.

Design language follows [NeoPanel](https://github.com/Pekly/NeoPanel). NeoLauncher is a separate project and does not embed the panel backend. Not affiliated with Mojang or Microsoft.

## License

MIT. See [LICENSE](LICENSE).
