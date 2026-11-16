# Minecraft installation and launch specifications

`New(root string, client *download.Client) *Service` exposes:

- `Versions(context.Context) ([]Version, error)`
- `InstallClient(context.Context, directory string, Options, download.Reporter) (Installation, error)`
- `InstallServer(context.Context, directory string, Options, download.Reporter) (Installation, error)`
- `ClientLaunch(directory, javaPath string, memoryMB int, Identity) (LaunchSpec, error)`
- `ServerLaunch(directory, javaPath string, memoryMB int) (LaunchSpec, error)`
- `ReadInstallation(directory string) (Installation, error)`

Only vanilla (including an empty loader option) and Fabric are supported. An empty Fabric loader version selects the first stable loader supported by the requested Minecraft version. Fabric uses its official client/server JSON profiles and verified Maven libraries; the server runs the profile's KnotServer main class against the verified vanilla server jar. No unverified generated installer/launcher is needed.

Installs use the official Mojang manifest, SHA-1-verified version metadata, jars, libraries, logging configuration, asset index and assets. Maven artifacts without embedded download metadata require a published `.sha1` sidecar; absent/bad checksums fail. Files are shared beneath `root/cache`, with six asset workers, first-error cancellation, serialized progress callbacks and per-destination cache locking. Asset indexes include their content hash in the local index name so vendor republication cannot invalidate an earlier profile. Legacy natives are extracted with path/symlink/duplicate/size/count limits. Modern native libraries remain on the metadata-defined classpath and self-extract through their vendor libraries.

A target must be empty or contain only regular `server.properties` and/or `eula.txt`. Those files are never changed. Downloads and extraction finish in a fresh staging directory before publishing; `.neolauncher-install.json` is the last success marker. Failed downloads remove only their fresh staging directory; a failure during final publication may leave launcher-owned files without the success marker and requires explicit caller/user recovery. Existing installs, worlds, mods or arbitrary files are never overwritten/deleted. The caller must serialize install/start operations for each profile and retain the same cache root.

Launch specifications are private in-memory data (all fields are excluded from JSON). They contain an executable and argument array, never a shell command. The only subprocess this package executes is a five-second, 64-KiB-bounded Java `-version` probe. Resolution uses configured executable, otherwise `JAVA_HOME/bin/java`, otherwise `PATH`; an explicitly configured invalid choice does not silently fall back. The Java major must exactly match the metadata requirement. Windows `javaw.exe` resolves to sibling `java.exe` for output and console control. No Java installation, system/environment changes or EULA acceptance occurs. Server launch requires a single explicit uncommented `eula=true` assignment and appends `nogui`; the parent owns spawning and stdin `stop`.

## Explicit limitations

- No Forge/NeoForge/Quilt, modpack, account/authentication, Java-download, repair/upgrade, demo, custom-resolution or Quick Play API.
- Legacy virtual/resource-mapped asset layouts and ambiguous legacy quoted argument strings fail explicitly. Current vanilla/Fabric profiles are supported; not every historical game version is promised.
- Known OS/architecture and feature rules are evaluated in order. Unknown rules/features or unresolved active placeholders fail closed. OS-version regex rules use the real Windows version; a relevant version-specific rule on another OS fails rather than guessing. Legacy x86 natives are rejected on ARM64.
- Optional vendor `default-user-jvm` tuning recommendations are not used: caller-specified heap limits and the installed JVM defaults are used instead. Required `arguments.jvm` and `arguments.game` are honored.
- No Windows Java argument-file workaround for exceptionally long caller-controlled cache paths. Keep the root reasonably short.
- Launch validates metadata and confined regular paths, and rechecks server jar / asset-index / logging hashes. It does not rehash every already-installed client library/asset on every launch or treat local writable metadata as a sandbox against its owner.

## Verification

`go test ./internal/minecraft` is offline, using synthetic vendor manifests/jars/assets, local HTTP fixtures and a test-binary Java responder. EULA-true contents exist only in disposable synthetic test directories, never a real server. Endpoint/platform/Java-resolver injection is package-private; production defaults stay restricted by `internal/download`.

`NEOLAUNCHER_TEST_VENDOR_METADATA=1 go test -run TestLiveVendorMetadata -v ./internal/minecraft` additionally checks live official metadata and Maven checksum sidecars only. It never downloads game/library jars, launches a game, or accepts a real EULA.
