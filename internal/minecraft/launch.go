package minecraft

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Pekly/NeoLauncher/internal/download"
)

func regularPath(root, relative string) (string, error) {
	p, err := download.SafeJoin(root, relative)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("installed file is not a regular file")
	}
	return p, nil
}
func (s *Service) loadRuntime(directory, kind string) (installedRuntime, string, string, error) {
	var rt installedRuntime
	dir, err := profileRoot(directory)
	if err != nil {
		return rt, "", "", err
	}
	root, err := profileRoot(s.root)
	if err != nil {
		return rt, "", "", err
	}
	i, err := s.ReadInstallation(dir)
	if err != nil {
		return rt, "", "", err
	}
	if i.Kind != kind {
		return rt, "", "", errors.New("installation kind does not match launch request")
	}
	p, err := regularPath(dir, runtimeFile)
	if err != nil {
		return rt, "", "", err
	}
	if err = readJSON(p, &rt); err != nil {
		return rt, "", "", err
	}
	if rt.Installation != i || rt.Metadata.ID != i.Version || rt.Metadata.JavaVersion.MajorVersion != i.JavaMajor || rt.Platform.OS != s.platform.OS || rt.Platform.Arch != s.platform.Arch {
		return rt, "", "", errors.New("installation metadata or platform mismatch")
	}
	if rt.NativeDir != ".neolauncher/natives" {
		return rt, "", "", errors.New("invalid native directory")
	}
	if kind == "server" && rt.ServerJar != "server.jar" {
		return rt, "", "", errors.New("invalid server jar path")
	}
	return rt, dir, root, nil
}
func classpath(root string, rt installedRuntime) (string, error) {
	if len(rt.Classpath) == 0 {
		return "", errors.New("empty installed classpath")
	}
	paths := make([]string, 0, len(rt.Classpath))
	for _, rel := range rt.Classpath {
		if !strings.HasPrefix(rel, "cache/libraries/") && !strings.HasPrefix(rel, "cache/versions/") {
			return "", errors.New("classpath is outside the game cache")
		}
		p, err := regularPath(root, rel)
		if err != nil {
			return "", err
		}
		if strings.ContainsRune(p, os.PathListSeparator) {
			return "", errors.New("classpath path contains a separator")
		}
		paths = append(paths, p)
	}
	return strings.Join(paths, string(os.PathListSeparator)), nil
}
func memoryArgs(memoryMB int) ([]string, error) {
	if memoryMB < 256 || memoryMB > 1048576 {
		return nil, errors.New("memory must be between 256 and 1048576 MiB")
	}
	initial := memoryMB
	if initial > 512 {
		initial = 512
	}
	return []string{fmt.Sprintf("-Xms%dM", initial), fmt.Sprintf("-Xmx%dM", memoryMB)}, nil
}
func (s *Service) ClientLaunch(directory, javaPath string, memoryMB int, identity Identity) (LaunchSpec, error) {
	var zero LaunchSpec
	mem, err := memoryArgs(memoryMB)
	if err != nil {
		return zero, err
	}
	if identity.Name == "" || identity.UUID == "" || identity.AccessToken == "" {
		return zero, errors.New("a signed-in Minecraft identity is required")
	}
	for _, v := range []string{identity.Name, identity.UUID, identity.AccessToken, identity.XUID} {
		if strings.ContainsAny(v, "\x00\r\n") {
			return zero, errors.New("invalid identity")
		}
	}
	rt, dir, root, err := s.loadRuntime(directory, "client")
	if err != nil {
		return zero, err
	}
	if !validMainClass(rt.Metadata.MainClass) {
		return zero, errors.New("invalid client main class")
	}
	cp, err := classpath(root, rt)
	if err != nil {
		return zero, err
	}
	native, err := download.SafeJoin(dir, rt.NativeDir)
	if err != nil {
		return zero, err
	}
	info, err := os.Stat(native)
	if err != nil || !info.IsDir() {
		return zero, errors.New("native directory is missing")
	}
	if !safeID(rt.Metadata.AssetIndex.ID) || !hashID(rt.Metadata.AssetIndex.SHA1) {
		return zero, errors.New("invalid installed asset index")
	}
	index, err := regularPath(root, "cache/assets/indexes/"+assetIndexName(rt.Metadata)+".json")
	if err != nil {
		return zero, err
	}
	if err = download.Verify(index, download.File{SHA1: rt.Metadata.AssetIndex.SHA1, Size: rt.Metadata.AssetIndex.Size}); err != nil {
		return zero, err
	}
	assets, err := download.SafeJoin(root, "cache/assets")
	if err != nil {
		return zero, err
	}
	libs, err := download.SafeJoin(root, "cache/libraries")
	if err != nil {
		return zero, err
	}
	values := map[string]string{
		"auth_player_name": identity.Name, "auth_uuid": identity.UUID, "auth_access_token": identity.AccessToken, "auth_xuid": identity.XUID, "clientid": "",
		"auth_session": "token:" + identity.AccessToken + ":" + identity.UUID, "user_type": "msa", "user_properties": "{}",
		"version_name": rt.Installation.Version, "version_type": rt.Metadata.Type, "game_directory": dir, "assets_root": assets, "assets_index_name": assetIndexName(rt.Metadata),
		"natives_directory": native, "library_directory": libs, "classpath": cp, "classpath_separator": string(os.PathListSeparator), "launcher_name": "NeoLauncher", "launcher_version": "0.2.0",
	}
	jvm, err := s.arguments(rt.Metadata.Arguments.JVM)
	if err != nil {
		return zero, err
	}
	if len(jvm) == 0 {
		jvm = []string{"-Djava.library.path=${natives_directory}", "-cp", "${classpath}"}
		if s.platform.OS == "osx" {
			jvm = append([]string{"-XstartOnFirstThread"}, jvm...)
		}
	}
	jvm, err = substitute(jvm, values)
	if err != nil {
		return zero, err
	}
	game, err := s.gameArguments(rt.Metadata)
	if err != nil {
		return zero, err
	}
	game, err = substitute(game, values)
	if err != nil {
		return zero, err
	}
	if log, ok := rt.Metadata.Logging["client"]; ok {
		if !strings.HasPrefix(rt.LoggingFile, "cache/logging/") {
			return zero, errors.New("invalid logging configuration path")
		}
		p, e := regularPath(root, rt.LoggingFile)
		if e != nil {
			return zero, e
		}
		if e = download.Verify(p, fileFor(log.File)); e != nil {
			return zero, e
		}
		args, e := substitute([]string{log.Argument}, map[string]string{"path": p})
		if e != nil {
			return zero, e
		}
		jvm = append(jvm, args...)
	} else if rt.LoggingFile != "" {
		return zero, errors.New("unexpected logging configuration")
	}
	// Do not duplicate the long classpath: Windows has a 32767-character process limit.
	hasClasspath := false
	for n, arg := range jvm {
		if arg == "-cp" || arg == "-classpath" || arg == "--class-path" {
			if hasClasspath || n+1 >= len(jvm) || jvm[n+1] != cp {
				return zero, errors.New("invalid or conflicting JVM classpath")
			}
			hasClasspath = true
		}
	}
	if !hasClasspath {
		jvm = append(jvm, "-cp", cp)
	}
	jvm = append(jvm, mem...)
	args := append(jvm, rt.Metadata.MainClass)
	args = append(args, game...)
	program, err := s.javaResolver(javaPath, rt.Installation.JavaMajor)
	if err != nil {
		return zero, err
	}
	secrets := []string{identity.AccessToken}
	if identity.XUID != "" {
		secrets = append(secrets, identity.XUID)
	}
	return LaunchSpec{Program: program, Args: args, Directory: dir, Secrets: secrets}, nil
}

// acceptedEULA deliberately accepts only one unambiguous, explicit assignment.
// Comments, escaped properties, duplicate keys and inline comments never count.
func acceptedEULA(dir string) error {
	p, err := regularPath(dir, "eula.txt")
	if err != nil {
		return errors.New("accept the Minecraft EULA explicitly in eula.txt before starting")
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > 64<<10 {
		return errors.New("invalid EULA file")
	}
	scan := bufio.NewScanner(f)
	found := false
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "eula" {
			return errors.New("ambiguous EULA file; use an explicit eula=true assignment")
		}
		if found || strings.TrimSpace(value) != "true" {
			return errors.New("Minecraft EULA has not been explicitly accepted")
		}
		found = true
	}
	if err = scan.Err(); err != nil {
		return err
	}
	if !found {
		return errors.New("Minecraft EULA has not been explicitly accepted")
	}
	return nil
}
func (s *Service) ServerLaunch(directory, javaPath string, memoryMB int) (LaunchSpec, error) {
	var zero LaunchSpec
	mem, err := memoryArgs(memoryMB)
	if err != nil {
		return zero, err
	}
	rt, dir, root, err := s.loadRuntime(directory, "server")
	if err != nil {
		return zero, err
	}
	if err = acceptedEULA(dir); err != nil {
		return zero, err
	}
	jar, err := regularPath(dir, rt.ServerJar)
	if err != nil {
		return zero, err
	}
	a, ok := rt.Metadata.Downloads["server"]
	if !ok {
		return zero, errors.New("missing server download metadata")
	}
	if err = download.Verify(jar, fileFor(a)); err != nil {
		return zero, err
	}
	args := mem
	if rt.Installation.Loader == "vanilla" {
		args = append(args, "-jar", jar)
	} else {
		if !validMainClass(rt.Metadata.MainClass) {
			return zero, errors.New("invalid Fabric server main class")
		}
		cp, e := classpath(root, rt)
		if e != nil {
			return zero, e
		}
		jvm, e := s.arguments(rt.Metadata.Arguments.JVM)
		if e != nil {
			return zero, e
		}
		jvm, e = substitute(jvm, map[string]string{"classpath": cp, "library_directory": filepath.Join(root, "cache", "libraries"), "launcher_name": "NeoLauncher", "launcher_version": "0.2.0"})
		if e != nil {
			return zero, e
		}
		args = append(jvm, mem...)
		args = append(args, "-Dfabric.gameJarPath="+jar, "-cp", cp, rt.Metadata.MainClass)
		game, e := s.arguments(rt.Metadata.Arguments.Game)
		if e != nil {
			return zero, e
		}
		game, e = substitute(game, map[string]string{})
		if e != nil {
			return zero, e
		}
		args = append(args, game...)
	}
	args = append(args, "nogui")
	program, err := s.javaResolver(javaPath, rt.Installation.JavaMajor)
	if err != nil {
		return zero, err
	}
	return LaunchSpec{Program: program, Args: args, Directory: dir}, nil
}
