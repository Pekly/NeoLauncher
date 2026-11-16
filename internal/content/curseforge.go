package content

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Pekly/NeoLauncher/internal/download"
)

const cfAPI = "https://api.curseforge.com/v1"

var errCFServer = errors.New("unsupported CurseForge server pack: an official serverPackFileId and usable manifest are required")

type cfProject struct {
	ID      int64  `json:"id"`
	GameID  int    `json:"gameId"`
	ClassID int    `json:"classId"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Logo    struct {
		URL string `json:"url"`
	} `json:"logo"`
	AllowModDistribution *bool `json:"allowModDistribution"`
}
type cfFile struct {
	ID               int64    `json:"id"`
	ModID            int64    `json:"modId"`
	DisplayName      string   `json:"displayName"`
	FileName         string   `json:"fileName"`
	DownloadURL      string   `json:"downloadUrl"`
	FileLength       int64    `json:"fileLength"`
	GameVersions     []string `json:"gameVersions"`
	IsAvailable      bool     `json:"isAvailable"`
	ServerPackFileID int64    `json:"serverPackFileId"`
	Hashes           []struct {
		Value string `json:"value"`
		Algo  int    `json:"algo"`
	} `json:"hashes"`
	Dependencies []struct {
		ModID        int64 `json:"modId"`
		RelationType int   `json:"relationType"`
	} `json:"dependencies"`
}

func cfID(raw string) (int64, error) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("invalid CurseForge project/file ID")
	}
	return n, nil
}
func (s *Service) cfJSON(ctx context.Context, endpoint string, dest any) error {
	if s.curseForgeEnabled == nil || !s.curseForgeEnabled() {
		return errors.New("CurseForge is disabled; enable it and supply an API key")
	}
	if s.curseForgeKey == nil {
		return errors.New("CurseForge API key is missing")
	}
	key, err := s.curseForgeKey()
	if err != nil {
		return errors.New("CurseForge API key could not be loaded")
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("CurseForge API key is missing")
	}
	// Reject API redirects even with caller-supplied transports/HTTP clients.
	client := *s.client
	base := client.HTTP
	if base == nil {
		base = download.New().HTTP
	}
	httpClient := *base
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("CurseForge API redirects are not supported")
	}
	client.HTTP = &httpClient
	err = client.JSON(ctx, cfAPI+endpoint, dest, map[string]string{"x-api-key": key})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var status *download.HTTPError
		if errors.As(err, &status) {
			return fmt.Errorf("CurseForge API request failed (HTTP %d)", status.Status)
		}
		return errors.New("CurseForge API request failed or returned invalid metadata")
	}
	return nil
}
func (s *Service) cfSearch(ctx context.Context, r SearchRequest) ([]Project, error) {
	class := "6"
	if r.Kind == "modpack" {
		class = "4471"
	}
	q := url.Values{"gameId": {"432"}, "classId": {class}, "searchFilter": {r.Query}, "pageSize": {"30"}, "sortField": {"2"}, "sortOrder": {"desc"}}
	if r.GameVersion != "" {
		q.Set("gameVersion", r.GameVersion)
	}
	if r.Loader == "fabric" {
		q.Set("modLoaderType", "4")
	}
	var response struct {
		Data []cfProject `json:"data"`
	}
	if err := s.cfJSON(ctx, "/mods/search?"+q.Encode(), &response); err != nil {
		return nil, err
	}
	result := []Project{}
	for _, p := range response.Data {
		if p.GameID != 432 || strconv.Itoa(p.ClassID) != class {
			continue
		}
		result = append(result, Project{"curseforge", fmt.Sprint(p.ID), p.Name, p.Summary, p.Logo.URL, r.Kind})
	}
	return result, nil
}
func (s *Service) cfVersions(ctx context.Context, project, game, loader string) ([]cfFile, error) {
	if _, err := cfID(project); err != nil {
		return nil, err
	}
	q := url.Values{"pageSize": {"50"}}
	if game != "" {
		q.Set("gameVersion", game)
	}
	if loader == "fabric" {
		q.Set("modLoaderType", "4")
	}
	var response struct {
		Data []cfFile `json:"data"`
	}
	err := s.cfJSON(ctx, "/mods/"+project+"/files?"+q.Encode(), &response)
	if err != nil {
		return nil, err
	}
	result := []cfFile{}
	for _, f := range response.Data {
		if f.IsAvailable && (game == "" || contains(f.GameVersions, game)) && (loader != "fabric" || contains(f.GameVersions, "Fabric")) {
			result = append(result, f)
		}
	}
	return result, nil
}
func (s *Service) cfGetProject(ctx context.Context, id int64) (cfProject, error) {
	var response struct {
		Data cfProject `json:"data"`
	}
	if id <= 0 {
		return response.Data, errors.New("invalid CurseForge dependency ID")
	}
	err := s.cfJSON(ctx, fmt.Sprintf("/mods/%d", id), &response)
	if err != nil {
		return response.Data, err
	}
	if response.Data.ID != id || response.Data.GameID != 432 {
		return response.Data, errors.New("CurseForge project is not a Minecraft project")
	}
	if response.Data.AllowModDistribution != nil && !*response.Data.AllowModDistribution {
		return response.Data, errors.New("CurseForge author has disabled third-party downloads")
	}
	return response.Data, nil
}
func (s *Service) cfChoose(ctx context.Context, project, file int64, game, loader string) (cfFile, error) {
	var f cfFile
	if file > 0 {
		var response struct {
			Data cfFile `json:"data"`
		}
		if err := s.cfJSON(ctx, fmt.Sprintf("/mods/%d/files/%d", project, file), &response); err != nil {
			return f, err
		}
		f = response.Data
		if f.ID != file {
			return f, errors.New("CurseForge returned a different file")
		}
	} else {
		versions, err := s.cfVersions(ctx, fmt.Sprint(project), game, loader)
		if err != nil {
			return f, err
		}
		if len(versions) > 0 {
			f = versions[0]
		}
	}
	if f.ID == 0 {
		return f, errors.New("no compatible CurseForge file is available")
	}
	if f.ModID != project {
		return f, errors.New("CurseForge file belongs to a different project")
	}
	if !f.IsAvailable || f.DownloadURL == "" {
		return f, errors.New("CurseForge file is unavailable or author has disabled third-party downloads")
	}
	if game != "" && !contains(f.GameVersions, game) || loader == "fabric" && !contains(f.GameVersions, "Fabric") {
		return f, errors.New("chosen CurseForge file is incompatible with the target Minecraft version or Fabric loader")
	}
	return f, nil
}
func cfDownload(f cfFile, kind string) (download.File, error) {
	extension := ".jar"
	prefix := "mods/"
	if kind == "modpack" {
		extension = ".zip"
		prefix = "packs/"
	}
	if !strings.HasSuffix(strings.ToLower(f.FileName), extension) || strings.ContainsAny(f.FileName, "/\\") || !download.SafeRelative(f.FileName) {
		return download.File{}, errors.New("CurseForge file has an unsafe or unsupported filename")
	}
	result := download.File{URL: f.DownloadURL, Path: prefix + f.FileName, Size: f.FileLength}
	for _, h := range f.Hashes {
		if h.Algo == 1 {
			result.SHA1 = h.Value
			break
		}
	}
	if result.SHA1 == "" {
		return result, errors.New("CurseForge file has no published SHA1 checksum")
	}
	return result, nil
}

type cfResolver struct {
	s      *Service
	ctx    context.Context
	r      InstallRequest
	p      *Plan
	seen   map[int64]int64
	pinned map[int64]int64
	edges  int
}

func (v *cfResolver) resolve(project, file int64, depth int) error {
	if err := v.ctx.Err(); err != nil {
		return err
	}
	if depth > maxDepth {
		return errors.New("CurseForge dependency depth limit exceeded")
	}
	if pinned, ok := v.pinned[project]; ok {
		if file != 0 && file != pinned {
			return errors.New("conflicting CurseForge dependency versions")
		}
		file = pinned
	}
	if previous, ok := v.seen[project]; ok {
		if file != 0 && file != previous {
			return errors.New("conflicting CurseForge dependency versions")
		}
		return nil
	}
	if len(v.seen) >= maxDependencies {
		return errors.New("CurseForge dependency count limit exceeded")
	}
	info, err := v.s.cfGetProject(v.ctx, project)
	if err != nil {
		return err
	}
	if info.ClassID != 6 {
		return errors.New("CurseForge dependency is not a mod")
	}
	f, err := v.s.cfChoose(v.ctx, project, file, v.r.GameVersion, v.r.Loader)
	if err != nil {
		return err
	}
	v.seen[project] = f.ID
	item, err := cfDownload(f, "mod")
	if err != nil {
		return err
	}
	v.p.Files = append(v.p.Files, item)
	v.edges += len(f.Dependencies)
	if v.edges > 1024 {
		return errors.New("CurseForge dependency edge limit exceeded")
	}
	for _, dep := range f.Dependencies {
		if dep.RelationType != 3 && !(dep.RelationType == 2 && v.r.IncludeOptional) {
			continue
		}
		if err = v.resolve(dep.ModID, 0, depth+1); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) cfPlan(ctx context.Context, r InstallRequest, p *Plan, report download.Reporter) error {
	id, err := cfID(r.ProjectID)
	if err != nil {
		return err
	}
	fileID := int64(0)
	if r.VersionID != "" {
		fileID, err = cfID(r.VersionID)
		if err != nil {
			return err
		}
	}
	project, err := s.cfGetProject(ctx, id)
	if err != nil {
		return err
	}
	p.Name = project.Name
	expected := 6
	if r.Kind == "modpack" {
		expected = 4471
	}
	if project.ClassID != expected {
		return errors.New("CurseForge project kind does not match request")
	}
	if r.Kind == "mod" {
		warning(p, "CurseForge does not publish reliable client/server mod compatibility; verify the author's side requirements.")
		resolver := cfResolver{s: s, ctx: ctx, r: r, p: p, seen: map[int64]int64{}}
		return resolver.resolve(id, fileID, 0)
	}
	// Pack metadata often lacks loader tags; the verified manifest is authoritative.
	f, err := s.cfChoose(ctx, id, fileID, "", "")
	if err != nil {
		return err
	}
	if r.Side == "server" {
		if f.ServerPackFileID <= 0 || f.ServerPackFileID == f.ID {
			return errCFServer
		}
		f, err = s.cfChoose(ctx, id, f.ServerPackFileID, "", "")
		if err != nil {
			return fmt.Errorf("%w: official server file unavailable", errCFServer)
		}
	}
	item, err := cfDownload(f, "modpack")
	if err != nil {
		return err
	}
	archive, err := s.fetchArchive(ctx, item, report)
	if err != nil {
		return err
	}
	return s.cfPack(ctx, archive, r, p)
}
