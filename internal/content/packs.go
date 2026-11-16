package content

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/Pekly/NeoLauncher/internal/download"
)

const maxArchiveBytes int64 = 128 << 20
const maxOverrideBytes uint64 = 64 << 20
const maxEntryBytes uint64 = 16 << 20
const maxEntries = 10000

type packArchive map[string][]byte

func (s *Service) fetchArchive(ctx context.Context, f download.File, report download.Reporter) (packArchive, error) {
	if err := s.validFile(f); err != nil {
		return nil, err
	}
	if f.Size > maxArchiveBytes {
		return nil, errors.New("pack archive exceeds 128 MiB compressed limit")
	}
	if report != nil {
		report(download.Progress{Stage: "Reading pack", Name: "Pack archive", TotalBytes: f.Size})
	}
	data, err := s.client.Fetch(ctx, f.URL, maxArchiveBytes, nil)
	if err != nil {
		return nil, err
	}
	if f.Size > 0 && int64(len(data)) != f.Size {
		return nil, errors.New("pack archive size does not match")
	}
	sha1sum := sha1.Sum(data)
	sha256sum := sha256.Sum256(data)
	sha512sum := sha512.Sum512(data)
	for _, h := range []struct {
		expected string
		sum      []byte
	}{{f.SHA1, sha1sum[:]}, {f.SHA256, sha256sum[:]}, {f.SHA512, sha512sum[:]}} {
		if h.expected != "" && !strings.EqualFold(h.expected, hex.EncodeToString(h.sum)) {
			return nil, errors.New("pack archive checksum does not match")
		}
	}
	return readArchive(ctx, data)
}
func readArchive(ctx context.Context, data []byte) (packArchive, error) {
	if int64(len(data)) > maxArchiveBytes {
		return nil, errors.New("pack archive exceeds compressed size limit")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("invalid pack ZIP archive")
	}
	if len(reader.File) > maxEntries {
		return nil, errors.New("pack archive has too many entries")
	}
	result := packArchive{}
	seen := map[string]bool{}
	files := map[string]bool{}
	var total uint64
	for _, entry := range reader.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name
		if entry.FileInfo().IsDir() {
			name = strings.TrimSuffix(name, "/")
		}
		if !safeDestination(name) {
			return nil, errors.New("unsafe pack archive path")
		}
		if !entry.Mode().IsRegular() && !entry.Mode().IsDir() {
			return nil, errors.New("pack archive symlinks and special files are not allowed")
		}
		key := strings.ToLower(name)
		if seen[key] {
			return nil, errors.New("duplicate case-insensitive pack archive path")
		}
		seen[key] = true
		if !entry.FileInfo().IsDir() {
			files[key] = true
		}
		if entry.UncompressedSize64 > maxEntryBytes {
			return nil, errors.New("pack entry exceeds 16 MiB limit")
		}
		if entry.UncompressedSize64 > maxOverrideBytes-total {
			return nil, errors.New("pack expanded data exceeds 64 MiB limit")
		}
		total += entry.UncompressedSize64
		if entry.FileInfo().IsDir() {
			if entry.UncompressedSize64 != 0 {
				return nil, errors.New("pack directory has unexpected data")
			}
			continue
		}
		stream, err := entry.Open()
		if err != nil {
			return nil, errors.New("cannot read pack archive entry")
		}
		body, readErr := io.ReadAll(io.LimitReader(stream, int64(maxEntryBytes)+1))
		closeErr := stream.Close()
		if readErr != nil || closeErr != nil || uint64(len(body)) != entry.UncompressedSize64 || uint64(len(body)) > maxEntryBytes {
			return nil, errors.New("pack entry checksum or expanded size does not match")
		}
		result[entry.Name] = body
	}
	for name := range seen {
		for prefix := name; strings.Contains(prefix, "/"); {
			prefix = prefix[:strings.LastIndex(prefix, "/")]
			if files[prefix] {
				return nil, errors.New("conflicting pack file/directory paths")
			}
		}
	}
	return result, nil
}
func addOverrides(a packArchive, p *Plan, prefixes []string) error {
	for name, data := range a {
		for _, prefix := range prefixes {
			if !strings.HasPrefix(name, prefix+"/") {
				continue
			}
			dest := strings.TrimPrefix(name, prefix+"/")
			if !safeDestination(dest) {
				return errors.New("unsafe pack override destination")
			}
			p.Overrides = append(p.Overrides, Override{Path: dest, Data: data})
			break
		}
	}
	return nil
}

type mrIndex struct {
	FormatVersion int               `json:"formatVersion"`
	Game          string            `json:"game"`
	Name          string            `json:"name"`
	Dependencies  map[string]string `json:"dependencies"`
	Files         []struct {
		Path      string            `json:"path"`
		Hashes    map[string]string `json:"hashes"`
		Env       map[string]string `json:"env"`
		Downloads []string          `json:"downloads"`
		FileSize  int64             `json:"fileSize"`
	} `json:"files"`
}

func (s *Service) mrPack(a packArchive, r InstallRequest, p *Plan) error {
	var index mrIndex
	data, ok := a["modrinth.index.json"]
	if !ok || json.Unmarshal(data, &index) != nil || index.FormatVersion != 1 || index.Game != "minecraft" {
		return errors.New("unsupported or invalid Modrinth pack index (Minecraft index v1 required)")
	}
	loader := "vanilla"
	loaderVersion := ""
	for name, version := range index.Dependencies {
		switch name {
		case "minecraft":
		case "fabric-loader":
			loader = "fabric"
			loaderVersion = version
		default:
			return errors.New("unsupported loader or dependency in pack: only Minecraft and Fabric are supported")
		}
	}
	if err := packTarget(p, r, index.Dependencies["minecraft"], loader, loaderVersion); err != nil {
		return err
	}
	if index.Name != "" {
		p.Name = index.Name
	}
	if len(index.Files) > maxEntries {
		return errors.New("pack index has too many files")
	}
	seen := map[string]bool{}
	for _, item := range index.Files {
		if !safeDestination(item.Path) {
			return errors.New("unsafe Modrinth pack file path")
		}
		key := strings.ToLower(item.Path)
		if seen[key] {
			return errors.New("duplicate Modrinth pack destination")
		}
		seen[key] = true
		for side, value := range item.Env {
			if side != "client" && side != "server" || value != "required" && value != "optional" && value != "unsupported" {
				return errors.New("invalid Modrinth pack environment rule")
			}
		}
		support := item.Env[r.Side]
		if support == "unsupported" || support == "optional" && !r.IncludeOptional {
			continue
		}
		if skipServer(item.Path, r.Side) {
			skipWarning(p)
			continue
		}
		if len(item.Downloads) == 0 {
			return errors.New("Modrinth pack file has no download URL")
		}
		f := download.File{Path: item.Path, SHA1: item.Hashes["sha1"], SHA512: item.Hashes["sha512"], Size: item.FileSize}
		// Pick only a published mirror approved by the shared vendor allowlist.
		found := false
		for _, raw := range item.Downloads {
			f.URL = raw
			if s.validFile(f) == nil {
				found = true
				break
			}
		}
		if !found {
			return errors.New("Modrinth pack file has no allowed URL with a valid published checksum")
		}
		p.Files = append(p.Files, f)
	}
	return addOverrides(a, p, []string{"overrides", r.Side + "-overrides"})
}

type cfManifest struct {
	ManifestType    string `json:"manifestType"`
	ManifestVersion int    `json:"manifestVersion"`
	Name            string `json:"name"`
	Overrides       string `json:"overrides"`
	Minecraft       struct {
		Version    string `json:"version"`
		ModLoaders []struct {
			ID      string `json:"id"`
			Primary bool   `json:"primary"`
		} `json:"modLoaders"`
	} `json:"minecraft"`
	Files []struct {
		ProjectID int64 `json:"projectID"`
		FileID    int64 `json:"fileID"`
		Required  bool  `json:"required"`
	} `json:"files"`
}

func (s *Service) cfPack(ctx context.Context, a packArchive, r InstallRequest, p *Plan) error {
	var manifest cfManifest
	data, ok := a["manifest.json"]
	if !ok || json.Unmarshal(data, &manifest) != nil || manifest.ManifestType != "minecraftModpack" || manifest.ManifestVersion != 1 || manifest.Minecraft.Version == "" {
		if r.Side == "server" {
			return errCFServer
		}
		return errors.New("unsupported or invalid CurseForge Minecraft manifest v1")
	}
	loader := "vanilla"
	loaderVersion := ""
	for _, entry := range manifest.Minecraft.ModLoaders {
		if !strings.HasPrefix(entry.ID, "fabric-") {
			return errors.New("unsupported loader: only vanilla and Fabric packs are supported")
		}
		version := strings.TrimPrefix(entry.ID, "fabric-")
		if loader == "fabric" && version != loaderVersion {
			return errors.New("pack declares conflicting Fabric loader versions")
		}
		loader = "fabric"
		loaderVersion = version
	}
	if err := packTarget(p, r, manifest.Minecraft.Version, loader, loaderVersion); err != nil {
		return err
	}
	if manifest.Name != "" {
		p.Name = manifest.Name
	}
	if len(manifest.Files) > maxDependencies {
		return errors.New("CurseForge pack exceeds dependency count limit")
	}
	if len(manifest.Files) > 0 && loader != "fabric" {
		return errors.New("CurseForge pack with mods requires a supported Fabric loader")
	}
	resolver := cfResolver{s: s, ctx: ctx, r: r, p: p, seen: map[int64]int64{}, pinned: map[int64]int64{}}
	resolver.r.GameVersion = p.Version
	resolver.r.Loader = p.Loader
	all := map[int64]bool{}
	for _, file := range manifest.Files {
		if file.ProjectID <= 0 || file.FileID <= 0 {
			return errors.New("invalid CurseForge manifest project/file ID")
		}
		if all[file.ProjectID] {
			return errors.New("duplicate CurseForge manifest project")
		}
		all[file.ProjectID] = true
		// An optional entry may still be pulled in by a required dependency.
		// Preserve its manifest pin without installing it merely for being listed.
		resolver.pinned[file.ProjectID] = file.FileID
	}
	for _, file := range manifest.Files {
		if !file.Required && !r.IncludeOptional {
			continue
		}
		if err := resolver.resolve(file.ProjectID, file.FileID, 0); err != nil {
			return err
		}
	}
	if manifest.Overrides == "" {
		return nil
	}
	if !safeDestination(manifest.Overrides) {
		return errors.New("unsafe CurseForge overrides prefix")
	}
	return addOverrides(a, p, []string{manifest.Overrides})
}
