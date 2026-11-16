package content

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/Pekly/NeoLauncher/internal/download"
)

const mrAPI = "https://api.modrinth.com/v2"

type mrProject struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	IconURL     string `json:"icon_url"`
	ProjectType string `json:"project_type"`
	ClientSide  string `json:"client_side"`
	ServerSide  string `json:"server_side"`
}
type mrFile struct {
	URL      string            `json:"url"`
	Filename string            `json:"filename"`
	Primary  bool              `json:"primary"`
	Size     int64             `json:"size"`
	Hashes   map[string]string `json:"hashes"`
}
type mrDependency struct {
	ProjectID string `json:"project_id"`
	VersionID string `json:"version_id"`
	Type      string `json:"dependency_type"`
}
type mrVersion struct {
	ID           string         `json:"id"`
	ProjectID    string         `json:"project_id"`
	Name         string         `json:"name"`
	GameVersions []string       `json:"game_versions"`
	Loaders      []string       `json:"loaders"`
	Files        []mrFile       `json:"files"`
	Dependencies []mrDependency `json:"dependencies"`
}

func (s *Service) mrSearch(ctx context.Context, r SearchRequest) ([]Project, error) {
	facets := [][]string{{"project_type:" + r.Kind}}
	if r.GameVersion != "" {
		facets = append(facets, []string{"versions:" + r.GameVersion})
	}
	if r.Loader != "" {
		facets = append(facets, []string{"categories:" + r.Loader})
	}
	if r.Kind == "mod" {
		facets = append(facets, []string{r.Side + "_side:required", r.Side + "_side:optional"})
	}
	encoded, _ := json.Marshal(facets)
	q := url.Values{"query": {r.Query}, "facets": {string(encoded)}, "limit": {"30"}}
	var response struct {
		Hits []struct {
			mrProject
			ProjectID string `json:"project_id"`
		} `json:"hits"`
	}
	if err := s.client.JSON(ctx, mrAPI+"/search?"+q.Encode(), &response, nil); err != nil {
		return nil, err
	}
	result := []Project{}
	for _, hit := range response.Hits {
		result = append(result, Project{"modrinth", hit.ProjectID, hit.Title, hit.Description, hit.IconURL, hit.ProjectType})
	}
	return result, nil
}
func (s *Service) mrVersions(ctx context.Context, projectID, game, loader string) ([]mrVersion, error) {
	q := url.Values{}
	if game != "" {
		v, _ := json.Marshal([]string{game})
		q.Set("game_versions", string(v))
	}
	if loader != "" {
		v, _ := json.Marshal([]string{loader})
		q.Set("loaders", string(v))
	}
	var result []mrVersion
	err := s.client.JSON(ctx, mrAPI+"/project/"+url.PathEscape(projectID)+"/version?"+q.Encode(), &result, nil)
	return result, err
}
func (s *Service) mrGetProject(ctx context.Context, id string) (mrProject, error) {
	var p mrProject
	if !validID(id) {
		return p, errors.New("invalid dependency project ID")
	}
	err := s.client.JSON(ctx, mrAPI+"/project/"+id, &p, nil)
	return p, err
}
func (s *Service) mrChoose(ctx context.Context, project, version, game, loader string) (mrVersion, error) {
	var v mrVersion
	if version != "" {
		if !validID(version) {
			return v, errors.New("invalid dependency version ID")
		}
		if err := s.client.JSON(ctx, mrAPI+"/version/"+version, &v, nil); err != nil {
			return v, err
		}
	} else {
		versions, err := s.mrVersions(ctx, project, game, loader)
		if err != nil {
			return v, err
		}
		for _, candidate := range versions {
			if (game == "" || contains(candidate.GameVersions, game)) && (loader == "" || contains(candidate.Loaders, loader)) {
				v = candidate
				break
			}
		}
	}
	if v.ID == "" {
		return v, errors.New("no compatible Modrinth version is available")
	}
	if version != "" && v.ID != version {
		return v, errors.New("Modrinth returned a different version")
	}
	if project != "" && v.ProjectID != project {
		return v, errors.New("Modrinth version belongs to a different project")
	}
	if game != "" && !contains(v.GameVersions, game) || loader != "" && !contains(v.Loaders, loader) {
		return v, errors.New("chosen Modrinth version is incompatible with the target Minecraft version or loader")
	}
	return v, nil
}
func mrDownload(v mrVersion, kind string) (download.File, error) {
	extension := ".jar"
	prefix := "mods/"
	if kind == "modpack" {
		extension = ".mrpack"
		prefix = "packs/"
	}
	var chosen *mrFile
	for i := range v.Files {
		f := &v.Files[i]
		if !strings.HasSuffix(strings.ToLower(f.Filename), extension) {
			continue
		}
		if strings.ContainsAny(f.Filename, "/\\") || !download.SafeRelative(f.Filename) {
			return download.File{}, errors.New("unsafe Modrinth filename")
		}
		if chosen == nil || f.Primary {
			chosen = f
		}
	}
	if chosen == nil {
		return download.File{}, errors.New("Modrinth version has no supported downloadable file")
	}
	return download.File{URL: chosen.URL, Path: prefix + chosen.Filename, Size: chosen.Size, SHA1: chosen.Hashes["sha1"], SHA512: chosen.Hashes["sha512"]}, nil
}
func mrSide(p mrProject, side string) error {
	support := p.ClientSide
	if side == "server" {
		support = p.ServerSide
	}
	if support != "required" && support != "optional" {
		return errors.New("Modrinth mod is unsupported or has unknown compatibility on the requested side")
	}
	return nil
}
func (s *Service) mrPlan(ctx context.Context, r InstallRequest, p *Plan, report download.Reporter) error {
	project, err := s.mrGetProject(ctx, r.ProjectID)
	if err != nil {
		return err
	}
	if project.ProjectType != r.Kind {
		return errors.New("Modrinth project kind does not match request")
	}
	p.Name = project.Title
	if r.Kind == "modpack" {
		v, err := s.mrChoose(ctx, r.ProjectID, r.VersionID, r.GameVersion, r.Loader)
		if err != nil {
			return err
		}
		f, err := mrDownload(v, "modpack")
		if err != nil {
			return err
		}
		archive, err := s.fetchArchive(ctx, f, report)
		if err != nil {
			return err
		}
		return s.mrPack(archive, r, p)
	}
	seen := map[string]string{}
	edges := 0
	var resolve func(string, string, int) error
	resolve = func(projectID, versionID string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > maxDepth {
			return errors.New("Modrinth dependency depth limit exceeded")
		}
		if previous, ok := seen[projectID]; ok && projectID != "" {
			if versionID != "" && versionID != previous {
				return errors.New("conflicting Modrinth dependency versions")
			}
			return nil
		}
		if len(seen) >= maxDependencies {
			return errors.New("Modrinth dependency count limit exceeded")
		}
		v, err := s.mrChoose(ctx, projectID, versionID, r.GameVersion, r.Loader)
		if err != nil {
			return err
		}
		if previous, ok := seen[v.ProjectID]; ok {
			if previous != v.ID {
				return errors.New("conflicting Modrinth dependency versions")
			}
			return nil
		}
		info, err := s.mrGetProject(ctx, v.ProjectID)
		if err != nil {
			return err
		}
		if info.ProjectType != "mod" {
			return errors.New("Modrinth dependency is not a mod")
		}
		if err = mrSide(info, r.Side); err != nil {
			return err
		}
		seen[v.ProjectID] = v.ID
		f, err := mrDownload(v, "mod")
		if err != nil {
			return err
		}
		p.Files = append(p.Files, f)
		edges += len(v.Dependencies)
		if edges > 1024 {
			return errors.New("Modrinth dependency edge limit exceeded")
		}
		for _, dep := range v.Dependencies {
			if dep.Type != "required" && !(dep.Type == "optional" && r.IncludeOptional) {
				continue
			}
			if dep.ProjectID == "" && dep.VersionID == "" {
				return errors.New("Modrinth dependency has no resolvable project/version ID")
			}
			if err = resolve(dep.ProjectID, dep.VersionID, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return resolve(r.ProjectID, r.VersionID, 0)
}
