package minecraft

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Pekly/NeoLauncher/internal/download"
)

func (s *Service) InstallClient(ctx context.Context, directory string, o Options, report download.Reporter) (Installation, error) {
	return s.install(ctx, directory, o, "client", report)
}
func (s *Service) InstallServer(ctx context.Context, directory string, o Options, report download.Reporter) (Installation, error) {
	return s.install(ctx, directory, o, "server", report)
}
func (s *Service) cached(ctx context.Context, relative string, f download.File, report download.Reporter) (string, error) {
	// Serialize the same cache destination across profiles without an unbounded lock map.
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(relative)))
	lock := &s.cacheLocks[h.Sum32()%uint32(len(s.cacheLocks))]
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	root, err := profileRoot(s.root)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(relative, "cache/") {
		return "", errors.New("invalid cache path")
	}
	dest, err := download.SafeJoin(root, relative)
	if err != nil {
		return "", err
	}
	if err = s.client.Download(ctx, f, dest, report); err != nil {
		return "", err
	}
	return dest, nil
}
func hashID(h string) bool             { b, err := hex.DecodeString(h); return err == nil && len(b) == 20 }
func fileFor(a artifact) download.File { return download.File{URL: a.URL, SHA1: a.SHA1, Size: a.Size} }
func (s *Service) versionMetadata(ctx context.Context, id string, report download.Reporter) (metadata, error) {
	var m metadata
	var list manifest
	emit(report, "Version metadata", id, 0, 1)
	if err := s.client.JSON(ctx, s.manifestURL, &list, nil); err != nil {
		return m, err
	}
	for _, v := range list.Versions {
		if v.ID == id {
			if !hashID(v.SHA1) {
				return m, errors.New("version metadata has no valid published hash")
			}
			p, err := s.cached(ctx, "cache/metadata/"+v.SHA1+".json", download.File{URL: v.URL, SHA1: v.SHA1}, nil)
			if err != nil {
				return m, err
			}
			if err = readJSON(p, &m); err != nil {
				return m, err
			}
			if m.ID != id || m.InheritsFrom != "" {
				return m, errors.New("unsupported version metadata inheritance or ID mismatch")
			}
			if m.JavaVersion.MajorVersion == 0 {
				m.JavaVersion.MajorVersion = 8
			}
			if m.JavaVersion.MajorVersion < 8 || m.JavaVersion.MajorVersion > 99 {
				return m, errors.New("unsupported Java requirement")
			}
			emit(report, "Version metadata", id, 1, 1)
			return m, nil
		}
	}
	return m, errors.New("Minecraft version is not in the official manifest")
}
func (s *Service) fabricProfile(ctx context.Context, o Options, kind string) (metadata, string, error) {
	var profile metadata
	var choices []struct {
		Loader struct {
			Version string `json:"version"`
			Stable  bool   `json:"stable"`
		} `json:"loader"`
	}
	base := strings.TrimRight(s.fabricURL, "/") + "/versions/loader/" + o.Version
	if err := s.client.JSON(ctx, base, &choices, nil); err != nil {
		return profile, "", err
	}
	chosen := o.LoaderVersion
	if chosen == "" {
		for _, c := range choices {
			if c.Loader.Stable {
				chosen = c.Loader.Version
				break
			}
		}
	}
	found := false
	for _, c := range choices {
		if c.Loader.Version == chosen {
			found = true
			break
		}
	}
	if !found || !safeID(chosen) {
		return profile, "", errors.New("requested stable Fabric loader is not available for this Minecraft version")
	}
	suffix := "/profile/json"
	if kind == "server" {
		suffix = "/server/json"
	}
	if err := s.client.JSON(ctx, base+"/"+chosen+suffix, &profile, nil); err != nil {
		return profile, "", err
	}
	if profile.InheritsFrom != "" && profile.InheritsFrom != o.Version {
		return profile, "", errors.New("Fabric profile inherits a different Minecraft version")
	}
	if !validMainClass(profile.MainClass) || len(profile.Libraries) == 0 {
		return profile, "", errors.New("incomplete Fabric profile")
	}
	return profile, chosen, nil
}
func mergeLibraries(base, overlay []library) []library {
	// Fabric's versions take precedence over the same Maven group/artifact/classifier.
	key := func(l library) string {
		p := strings.Split(l.Name, ":")
		if len(p) >= 3 {
			suffix := ""
			if len(p) > 3 {
				suffix = ":" + p[3]
			}
			return p[0] + ":" + p[1] + suffix
		}
		return l.Name
	}
	replaced := map[string]bool{}
	for _, l := range overlay {
		replaced[key(l)] = true
	}
	out := make([]library, 0, len(base)+len(overlay))
	for _, l := range base {
		if !replaced[key(l)] {
			out = append(out, l)
		}
	}
	return append(out, overlay...)
}
func (s *Service) install(ctx context.Context, directory string, o Options, kind string, report download.Reporter) (Installation, error) {
	var zero Installation
	o, err := normalized(o)
	if err != nil {
		return zero, err
	}
	if s.platform.OS != "windows" && s.platform.OS != "linux" && s.platform.OS != "osx" {
		return zero, errors.New("unsupported operating system")
	}
	if s.platform.Arch != "amd64" && s.platform.Arch != "386" && s.platform.Arch != "arm64" {
		return zero, errors.New("unsupported architecture")
	}
	dir, err := prepareDirectory(directory)
	if err != nil {
		return zero, err
	}
	m, err := s.versionMetadata(ctx, o.Version, report)
	if err != nil {
		return zero, err
	}
	jar, ok := m.Downloads[kind]
	if !ok || !hashID(jar.SHA1) {
		return zero, errors.New("this version has no verified " + kind + " download")
	}
	if o.Loader == "fabric" {
		emit(report, "Fabric metadata", o.Version, 0, 1)
		f, loader, e := s.fabricProfile(ctx, o, kind)
		if e != nil {
			return zero, e
		}
		o.LoaderVersion = loader
		if kind == "server" {
			m.Libraries = f.Libraries
			m.Arguments = f.Arguments
			m.Logging = nil
		} else {
			m.Libraries = mergeLibraries(m.Libraries, f.Libraries)
			m.Arguments.JVM = append(m.Arguments.JVM, f.Arguments.JVM...)
			m.Arguments.Game = append(m.Arguments.Game, f.Arguments.Game...)
		}
		m.MainClass = f.MainClass
		if f.JavaVersion.MajorVersion > m.JavaVersion.MajorVersion {
			m.JavaVersion = f.JavaVersion
		}
		emit(report, "Fabric metadata", loader, 1, 1)
	}
	i := Installation{Version: o.Version, Loader: o.Loader, LoaderVersion: o.LoaderVersion, JavaMajor: m.JavaVersion.MajorVersion, Kind: kind}
	rt := installedRuntime{Installation: i, Platform: s.platform, Metadata: m, NativeDir: ".neolauncher/natives"}
	// Only this fresh, private staging directory is removed on failure, never caller files.
	stage, err := os.MkdirTemp(dir, ".neolauncher-stage-")
	if err != nil {
		return zero, err
	}
	defer os.RemoveAll(stage)
	if err = os.Mkdir(filepath.Join(stage, "natives"), 0700); err != nil {
		return zero, err
	}
	jarRel := "cache/versions/" + jar.SHA1 + ".jar"
	emit(report, "Game jar", kind, 0, 1)
	jarPath, err := s.cached(ctx, jarRel, fileFor(jar), nil)
	if err != nil {
		return zero, err
	}
	emit(report, "Game jar", kind, 1, 1)
	if kind == "client" || o.Loader == "fabric" {
		cp, e := s.installLibraries(ctx, m.Libraries, filepath.Join(stage, "natives"), report)
		if e != nil {
			return zero, e
		}
		rt.Classpath = cp
	}
	if kind == "client" {
		rt.Classpath = append(rt.Classpath, jarRel)
		if err = s.installAssets(ctx, m, report); err != nil {
			return zero, err
		}
		if log, ok := m.Logging["client"]; ok {
			if !hashID(log.File.SHA1) {
				return zero, errors.New("logging configuration has no published hash")
			}
			rt.LoggingFile = "cache/logging/" + log.File.SHA1 + ".xml"
			emit(report, "Logging configuration", log.File.SHA1, 0, 1)
			if _, err = s.cached(ctx, rt.LoggingFile, fileFor(log.File), nil); err != nil {
				return zero, err
			}
			emit(report, "Logging configuration", log.File.SHA1, 1, 1)
		}
		// Fail installation rather than persist a launch configuration we cannot interpret.
		if _, err = s.arguments(m.Arguments.JVM); err != nil {
			return zero, err
		}
		if _, err = s.gameArguments(m); err != nil {
			return zero, err
		}
		if !validMainClass(m.MainClass) {
			return zero, errors.New("invalid client main class")
		}
	} else {
		rt.ServerJar = "server.jar"
		if err = copyNew(jarPath, filepath.Join(stage, "server.jar")); err != nil {
			return zero, err
		}
	}
	if err = ctx.Err(); err != nil {
		return zero, err
	}
	owned, err := download.SafeJoin(dir, ".neolauncher")
	if err != nil {
		return zero, err
	}
	if _, e := os.Lstat(owned); !errors.Is(e, os.ErrNotExist) {
		return zero, errors.New("installation directory appeared during install")
	}
	if err = os.Rename(stage, owned); err != nil {
		return zero, err
	}
	committed := false
	var published []string
	defer func() {
		if !committed {
			// Only files successfully created by this attempt are rolled back.
			for _, path := range published {
				_ = os.Remove(path)
			}
			_ = os.RemoveAll(owned)
		}
	}()
	if kind == "server" {
		dest, e := download.SafeJoin(dir, "server.jar")
		if e != nil {
			return zero, e
		}
		if err = copyNew(filepath.Join(owned, "server.jar"), dest); err != nil {
			return zero, err
		}
		published = append(published, dest)
	}
	if err = writeJSON(dir, runtimeFile, rt); err != nil {
		return zero, err
	}
	published = append(published, filepath.Join(dir, runtimeFile))
	// The public success marker is always last. No credentials are part of either record.
	if err = writeJSON(dir, installationFile, i); err != nil {
		return zero, err
	}
	committed = true
	emit(report, "Installed", o.Version, 1, 1)
	return i, nil
}
func copyNew(source, dest string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(dest)
		return errors.Join(err, closeErr)
	}
	return nil
}
func mavenPath(name string) (string, error) {
	p := strings.Split(name, ":")
	if len(p) < 3 || len(p) > 4 {
		return "", errors.New("unsupported Maven coordinate")
	}
	for _, v := range p {
		if !safeID(v) || strings.Contains(v, "@") {
			return "", errors.New("invalid Maven coordinate")
		}
	}
	suffix := ""
	if len(p) == 4 {
		suffix = "-" + p[3]
	}
	path := strings.ReplaceAll(p[0], ".", "/") + "/" + p[1] + "/" + p[2] + "/" + p[1] + "-" + p[2] + suffix + ".jar"
	if !download.SafeRelative(path) {
		return "", errors.New("unsafe Maven path")
	}
	return path, nil
}
func (s *Service) libraryArtifact(ctx context.Context, l library) (artifact, error) {
	if l.Downloads.Artifact != nil {
		a := *l.Downloads.Artifact
		if !download.SafeRelative(a.Path) || !hashID(a.SHA1) {
			return a, errors.New("library path or checksum is invalid")
		}
		return a, nil
	}
	path, err := mavenPath(l.Name)
	if err != nil {
		return artifact{}, err
	}
	base := l.URL
	if base == "" {
		base = "https://libraries.minecraft.net/"
	}
	raw := strings.TrimRight(base, "/") + "/" + path
	b, err := s.client.Fetch(ctx, raw+".sha1", 1024, nil)
	if err != nil {
		return artifact{}, fmt.Errorf("library %s has no accessible published SHA-1: %w", l.Name, err)
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 || !hashID(fields[0]) {
		return artifact{}, errors.New("invalid Maven checksum sidecar")
	}
	return artifact{Path: path, URL: raw, SHA1: fields[0]}, nil
}
func (s *Service) installLibraries(ctx context.Context, libs []library, natives string, report download.Reporter) ([]string, error) {
	var cp []string
	type job struct {
		a        artifact
		native   bool
		excludes []string
	}
	var jobs []job
	for _, l := range libs {
		allowed, err := s.allowed(l.Rules)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		// Legacy native-only entries can have no ordinary artifact.
		if l.Downloads.Artifact != nil || len(l.Natives) == 0 {
			a, e := s.libraryArtifact(ctx, l)
			if e != nil {
				return nil, e
			}
			jobs = append(jobs, job{a: a})
		}
		if classifier, ok := l.Natives[s.platform.OS]; ok {
			if s.platform.Arch == "arm64" && !strings.Contains(classifier, "arm64") && !strings.Contains(classifier, "aarch64") {
				return nil, errors.New("legacy natives do not support ARM64")
			}
			bits := "64"
			if s.platform.Arch == "386" {
				bits = "32"
			}
			classifier = strings.ReplaceAll(classifier, "${arch}", bits)
			a, found := l.Downloads.Classifiers[classifier]
			if !found || !download.SafeRelative(a.Path) || !hashID(a.SHA1) {
				return nil, errors.New("missing or invalid native classifier")
			}
			jobs = append(jobs, job{a: a, native: true, excludes: l.Extract.Exclude})
		}
	}
	emit(report, "Libraries", "", 0, len(jobs))
	seen := map[string]bool{}
	for n, j := range jobs {
		rel := "cache/libraries/" + j.a.Path
		p, err := s.cached(ctx, rel, fileFor(j.a), nil)
		if err != nil {
			return nil, err
		}
		if j.native {
			if err = extractNatives(p, natives, j.excludes); err != nil {
				return nil, err
			}
		} else if !seen[rel] {
			cp = append(cp, rel)
			seen[rel] = true
		}
		emit(report, "Libraries", j.a.Path, n+1, len(jobs))
	}
	return cp, nil
}

type assetIndex struct {
	Objects map[string]struct {
		Hash string `json:"hash"`
		Size int64  `json:"size"`
	} `json:"objects"`
	Virtual        bool `json:"virtual"`
	MapToResources bool `json:"map_to_resources"`
}

func (s *Service) installAssets(ctx context.Context, m metadata, report download.Reporter) error {
	a := m.AssetIndex
	if !safeID(a.ID) || !hashID(a.SHA1) {
		return errors.New("missing or invalid asset index")
	}
	emit(report, "Asset index", a.ID, 0, 1)
	// Mojang can republish an index ID with different contents; retain both hashes.
	p, err := s.cached(ctx, "cache/assets/indexes/"+assetIndexName(m)+".json", download.File{URL: a.URL, SHA1: a.SHA1, Size: a.Size}, nil)
	if err != nil {
		return err
	}
	var index assetIndex
	if err = readJSON(p, &index); err != nil {
		return err
	}
	if index.Virtual || index.MapToResources {
		return errors.New("legacy virtual/resource-mapped assets are not supported")
	}
	emit(report, "Asset index", a.ID, 1, 1)
	sizes := map[string]int64{}
	for name, obj := range index.Objects {
		if !download.SafeRelative(name) || !hashID(obj.Hash) || obj.Size < 0 || obj.Size > download.MaxFileBytes {
			return errors.New("invalid asset entry")
		}
		if size, ok := sizes[obj.Hash]; ok && size != obj.Size {
			return errors.New("conflicting asset sizes")
		}
		sizes[obj.Hash] = obj.Size
	}
	hashes := make([]string, 0, len(sizes))
	for hash := range sizes {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	emit(report, "Assets", "", 0, len(hashes))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	done := 0
	for n := 0; n < 6; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for hash := range jobs {
				if ctx.Err() != nil {
					return
				}
				rel := hash[:2] + "/" + hash
				_, e := s.cached(ctx, "cache/assets/objects/"+rel, download.File{URL: strings.TrimRight(s.assetURL, "/") + "/" + rel, SHA1: hash, Size: sizes[hash]}, nil)
				mu.Lock()
				if e != nil {
					if first == nil {
						first = e
						cancel()
					}
					mu.Unlock()
					return
				}
				done++
				emit(report, "Assets", hash, done, len(hashes))
				mu.Unlock()
			}
		}()
	}
feed:
	for _, hash := range hashes {
		select {
		case jobs <- hash:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}

func assetIndexName(m metadata) string { return m.AssetIndex.ID + "-" + m.AssetIndex.SHA1 }
