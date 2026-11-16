// Package content builds verified installation plans without touching live profiles.
package content

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/Pekly/NeoLauncher/internal/download"
)

const maxDependencies = 128
const maxDepth = 32

type SearchRequest struct {
	Provider    string `json:"provider"`
	Kind        string `json:"kind"`
	Query       string `json:"query"`
	GameVersion string `json:"game_version"`
	Loader      string `json:"loader"`
	Side        string `json:"side"`
}
type Project struct {
	Provider    string `json:"provider"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IconURL     string `json:"icon_url"`
	Kind        string `json:"kind"`
}
type Version struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type InstallRequest struct {
	Provider        string `json:"provider"`
	Kind            string `json:"kind"`
	ProjectID       string `json:"project_id"`
	VersionID       string `json:"version_id"`
	GameVersion     string `json:"game_version"`
	Loader          string `json:"loader"`
	Side            string `json:"side"`
	IncludeOptional bool   `json:"include_optional"`
}
type Override struct {
	Path string
	Data []byte `json:"-"`
}

// Plan is backend-only; the caller applies it under its profile operation lock.
type Plan struct {
	Name, Version, Loader, LoaderVersion string
	Files                                []download.File
	Overrides                            []Override
	Warnings                             []string
}
type Service struct {
	client            *download.Client
	curseForgeKey     func() (string, error)
	curseForgeEnabled func() bool
}

func New(client *download.Client, curseForgeKey func() (string, error), curseForgeEnabled func() bool) *Service {
	if client == nil {
		client = download.New()
	}
	return &Service{client: client, curseForgeKey: curseForgeKey, curseForgeEnabled: curseForgeEnabled}
}
func validate(provider, kind, loader, side string) error {
	if provider != "modrinth" && provider != "curseforge" {
		return errors.New("unsupported content provider")
	}
	if kind != "mod" && kind != "modpack" {
		return errors.New("unsupported content kind")
	}
	if side != "client" && side != "server" {
		return errors.New("content side must be client or server")
	}
	if loader != "" && loader != "vanilla" && loader != "fabric" {
		return errors.New("unsupported loader: only vanilla and Fabric are supported")
	}
	return nil
}
func (s *Service) Search(ctx context.Context, r SearchRequest) ([]Project, error) {
	if err := validate(r.Provider, r.Kind, r.Loader, r.Side); err != nil {
		return nil, err
	}
	if r.Provider == "modrinth" {
		return s.mrSearch(ctx, r)
	}
	return s.cfSearch(ctx, r)
}
func (s *Service) Versions(ctx context.Context, provider, projectID, gameVersion, loader string) ([]Version, error) {
	if err := validate(provider, "mod", loader, "client"); err != nil {
		return nil, err
	}
	if !validID(projectID) {
		return nil, errors.New("invalid project ID")
	}
	result := []Version{}
	if provider == "modrinth" {
		versions, err := s.mrVersions(ctx, projectID, gameVersion, loader)
		if err != nil {
			return nil, err
		}
		for _, v := range versions {
			result = append(result, Version{v.ID, v.Name})
		}
	} else {
		versions, err := s.cfVersions(ctx, projectID, gameVersion, loader)
		if err != nil {
			return nil, err
		}
		for _, v := range versions {
			result = append(result, Version{fmt.Sprint(v.ID), v.DisplayName})
		}
	}
	return result, nil
}
func (s *Service) Plan(ctx context.Context, r InstallRequest, workDir string, report download.Reporter) (*Plan, error) {
	if err := validate(r.Provider, r.Kind, r.Loader, r.Side); err != nil {
		return nil, err
	}
	if !validID(r.ProjectID) || (r.VersionID != "" && !validID(r.VersionID)) {
		return nil, errors.New("invalid project or version ID")
	}
	if r.Kind == "mod" && (r.GameVersion == "" || r.Loader != "fabric") {
		return nil, errors.New("mods require an exact Minecraft version and Fabric target")
	}
	// Archives are bounded and verified entirely in memory, so no staging or live files are written.
	_ = workDir
	p := &Plan{Version: r.GameVersion, Loader: r.Loader, Files: []download.File{}, Overrides: []Override{}, Warnings: []string{}}
	var err error
	if r.Provider == "modrinth" {
		err = s.mrPlan(ctx, r, p, report)
	} else {
		err = s.cfPlan(ctx, r, p, report)
	}
	if err != nil {
		return nil, err
	}
	if err = s.validatePlan(p, r.Side); err != nil {
		return nil, err
	}
	return p, nil
}
func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func safeDestination(name string) bool {
	if !download.SafeRelative(name) || strings.ContainsAny(name, "<>\"|?*") {
		return false
	}
	for _, c := range name {
		if c < 32 || c == 127 {
			return false
		}
	}
	parts := strings.Split(strings.ToLower(name), "/")
	for _, part := range parts {
		if strings.HasPrefix(part, ".neolauncher") || strings.HasPrefix(part, "launcher_") || strings.HasPrefix(part, "launcher-") || strings.HasPrefix(part, "launcher.") || part == "launcher" {
			return false
		}
		// Win32 also recognizes superscript device numbers and console aliases.
		base := strings.TrimRight(strings.SplitN(part, ".", 2)[0], " ")
		switch base {
		case "con", "prn", "aux", "nul", "conin$", "conout$", "clock$", "com¹", "com²", "com³", "lpt¹", "lpt²", "lpt³":
			return false
		}
	}
	switch parts[0] {
	case "versions", "libraries", "assets", "runtime", "natives", "accounts.json", "credentials.json", "fabric-server-launcher.properties":
		return false
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".exe", ".dll", ".bat", ".cmd", ".ps1", ".sh", ".lnk", ".url":
		return false
	}
	if len(parts) == 1 && strings.HasSuffix(parts[0], ".jar") {
		return false
	}
	return true
}
func skipServer(name, side string) bool {
	base := strings.ToLower(path.Base(name))
	return side == "server" && (base == "eula.txt" || base == "server.properties")
}
func warning(p *Plan, text string) {
	if !contains(p.Warnings, text) {
		p.Warnings = append(p.Warnings, text)
	}
}
func skipWarning(p *Plan) {
	warning(p, "Skipped pack eula.txt/server.properties; server consent and settings remain under launcher control.")
}
func (s *Service) validFile(f download.File) error {
	if !safeDestination(f.Path) {
		return errors.New("unsafe content destination")
	}
	u, err := url.Parse(f.URL)
	if err != nil || !download.AllowedURL(u) || (s.client.AllowURL != nil && !s.client.AllowURL(u)) {
		return errors.New("content download URL is not allowed")
	}
	if f.Size < 0 || f.Size > download.MaxFileBytes {
		return errors.New("content file exceeds size limit")
	}
	hasHash := false
	for _, h := range []struct {
		value string
		size  int
	}{{f.SHA1, 20}, {f.SHA512, 64}, {f.SHA256, 32}} {
		if h.value == "" {
			continue
		}
		b, err := hex.DecodeString(h.value)
		if err != nil || len(b) != h.size {
			return errors.New("invalid published content checksum")
		}
		hasHash = true
	}
	if !hasHash || f.AllowUnverified || f.AllowOverwrite {
		return errors.New("content requires a published checksum and no-overwrite policy")
	}
	return nil
}
func (s *Service) validatePlan(p *Plan, side string) error {
	seen := map[string]bool{}
	parents := map[string]bool{}
	add := func(name string) error {
		if !safeDestination(name) {
			return errors.New("unsafe content destination")
		}
		key := strings.ToLower(name)
		if seen[key] {
			return errors.New("duplicate content destination")
		}
		if parents[key] {
			return errors.New("conflicting content destinations")
		}
		for prefix := key; strings.Contains(prefix, "/"); {
			prefix = prefix[:strings.LastIndex(prefix, "/")]
			if seen[prefix] {
				return errors.New("conflicting content destinations")
			}
			parents[prefix] = true
		}
		seen[key] = true
		return nil
	}
	files := p.Files[:0]
	for _, f := range p.Files {
		if err := s.validFile(f); err != nil {
			return err
		}
		if err := add(f.Path); err != nil {
			return err
		}
		if skipServer(f.Path, side) {
			skipWarning(p)
			continue
		}
		files = append(files, f)
	}
	p.Files = files
	overrides := p.Overrides[:0]
	for _, o := range p.Overrides {
		if err := add(o.Path); err != nil {
			return err
		}
		if skipServer(o.Path, side) {
			skipWarning(p)
			continue
		}
		overrides = append(overrides, o)
	}
	p.Overrides = overrides
	return nil
}
func packTarget(p *Plan, r InstallRequest, game, loader, loaderVersion string) error {
	if game == "" {
		return errors.New("pack has no Minecraft version")
	}
	if loader != "vanilla" && loader != "fabric" {
		return errors.New("unsupported loader: only vanilla and Fabric packs are supported")
	}
	if loader == "fabric" && loaderVersion == "" {
		return errors.New("pack has no Fabric loader version")
	}
	if r.GameVersion != "" && r.GameVersion != game {
		return errors.New("pack Minecraft version does not match target")
	}
	if r.Loader != "" && r.Loader != loader {
		return errors.New("pack loader does not match target")
	}
	p.Version = game
	p.Loader = loader
	p.LoaderVersion = loaderVersion
	return nil
}
