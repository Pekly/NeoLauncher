# NeoLauncher

A Windows-first Minecraft Java launcher with the NeoPanel look, a skyblock waterfall, and lightweight local servers.

## What works

- **Play:** separate Vanilla/Fabric instances, verified game files, libraries, assets, and natives.
- **Servers:** install Vanilla/Fabric, explicitly accept the EULA, start/stop, read logs, and send console commands.
- **Mods and modpacks:** browse Modrinth or opt-in CurseForge for client and server profiles. Required dependencies are included; optional pack files can be included. Packs create new profiles.
- **Microsoft accounts:** official device-code sign-in, ownership checks, account selection, and refresh support. Passwords are entered only on Microsoft's site.
- **Local players:** named offline identities for installed games, separate from Microsoft accounts. No token is used and no automatic identity fallback occurs.
- **Settings:** Java path, memory, and a write-only CurseForge API key. Tokens and keys use Windows DPAPI, not plaintext storage.

This is an unsigned development build. Microsoft device-code creation works in the latest registration probe, but full account sign-in and authenticated gameplay have **not** been verified. Real CurseForge requests have not been tested with a user key.

## Quick start

1. Open `NeoLauncher.exe`. Microsoft Edge WebView2 Runtime is required (normally included with Windows).
2. Install Java separately. Choose its executable in **Settings**, or use `JAVA_HOME`/`PATH`. NeoLauncher checks the Java major required by the chosen Minecraft version.
3. For a server, open **Servers**, create a profile, and choose **Install**. Starting it asks you to review and accept the [Minecraft EULA](https://aka.ms/MinecraftEULA).
4. For a game, choose a **Microsoft** account or add a **Local** player, create a game profile, install it, and choose **Play**. Microsoft mode requires a Minecraft Java entitlement.
5. Open **Mods & packs** to add compatible mods or create a new modpack profile.

### Offline/local play

Add a local player on **Play**, using 1–16 letters, numbers, or underscores. Select that player and launch an installed game profile. Java and all game files must already be available; installation and content downloads still need internet.

Local names have stable Minecraft offline IDs. Keep the same spelling/case to retain that identity. Removing a local player does not delete worlds; adding the same name again restores the same local ID. Microsoft and local players are different identities, so their inventories may differ in the same world.

Local mode does not provide Microsoft skins or authenticated multiplayer. Local launches never use a Microsoft token, change your servers' `online-mode=true`, or silently take over after a failed Microsoft login.

### Microsoft app registration

The current build uses public client ID `af8942e2-c4a6-46b3-9085-5c657293f1d0`. Its owner must enable **Entra → App registrations → NeoLauncher → Authentication → Allow public client flows**, with personal Microsoft accounts supported. No client secret is needed or embedded.

The latest public registration probe successfully returned a device flow with Microsoft's official verification URL. This only verifies that sign-in can start; it does not prove a real account can finish approval or access Minecraft.

If Microsoft's website says **“There's a temporary problem” after approval**, close the sign-in dialog and request a fresh code. Try the official link in a private browser window with the intended personal Microsoft account. If it persists, try later; NeoLauncher cannot bypass or repair Microsoft's approval service. Do not share your code or tokens. Native errors now identify Microsoft, Xbox, or Minecraft and distinguish temporary outages, rate limits, and rejected permissions.

### CurseForge

CurseForge starts disabled. Save your own API key in **Settings**, then enable it. The key is masked, write-only, and sent only to the official CurseForge API. Author-disabled downloads are refused. Modrinth needs no key.

## Safety and limits

- Vanilla and Fabric only. Forge, NeoForge, Quilt, and incompatible packs are rejected.
- Server packs must support servers. A CurseForge modpack needs its author's separate server pack; client-only packs are not installed as servers.
- Downloads are bounded and verified against published checksums. Archives reject unsafe paths. Failed jobs report errors and can be retried; existing conflicting files are not overwritten.
- Pack installs use **new profiles**, preserving existing worlds/configuration. Installing or running profiles cannot be modified through the launcher.
- No automatic EULA acceptance, elevation, firewall/service changes, or Java installation. Server profiles start with `online-mode=true` and `eula=false`.
- Games and mods run with your Windows permissions. Job Objects own process trees and enforce memory limits; they are **not a security sandbox**. Install only trusted content.
- Server shutdown sends `stop` before a bounded force-stop. Closing games requires confirmation and can lose unsaved progress.
- This is local hosting, not NeoPanel administration or remote-node management. Network access still depends on your own network/firewall setup.

Settings and profiles live in `%LOCALAPPDATA%\NeoLauncher`, separate from NeoPanel and existing Minecraft files. To use isolated data:

```powershell
.\NeoLauncher.exe -data-dir 'D:\NeoLauncher Test'
```

## Build and test

Requires Windows and Go 1.26+. Go is needed only to build; no Node bundler or Wails CLI is required.

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\build-windows.ps1
.\dist\windows-amd64\NeoLauncher.exe

go test ./...
go vet ./...
node --test tests/*.test.cjs
```

Browser tests require Chrome or Edge; set `CHROME_BIN` if it is not found automatically. They use fake native bindings. Opening `internal/app/frontend/index.html` directly gives a read-only layout preview, not a working launcher.

`-Architecture arm64` produces an ARM64 cross-build; ARM64 hardware has not been validated. The native desktop targets Windows; non-Windows account storage fails closed.

Verified during development: package tests, safe paths/checksums, DPAPI storage, process ownership, and a synthetic Java install/start/command/save/stop lifecycle. Opt-in live tests downloaded stopped Vanilla/Fabric 1.21.1 servers and verified a Modrinth Fabric API file; no real Minecraft world or EULA was used in those checks.

Local mode is tested with a synthetic Java client while the Microsoft account service is closed, plus stable-ID, persistence, validation, and no-fallback tests. This does not claim real Minecraft gameplay validation.

Design language follows [NeoPanel](https://github.com/Pekly/NeoPanel). NeoLauncher is a separate project and does not embed the panel backend. Not affiliated with Mojang or Microsoft.

## License

MIT. See [LICENSE](LICENSE).
