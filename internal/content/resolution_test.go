package content

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestCFStandaloneRequiredDependenciesAndVerifiedDownloads(t *testing.T) {
	routes := map[string][]byte{}
	for i := 1; i <= 3; i++ {
		f := cfFileData(i, i*10, fmt.Sprintf("mod%d.jar", i), []byte("jar"))
		if i == 1 {
			f["dependencies"] = []map[string]any{{"modId": 2, "relationType": 3}, {"modId": 3, "relationType": 2}}
		}
		if i == 2 {
			f["dependencies"] = []map[string]any{{"modId": 1, "relationType": 3}}
		}
		routes[fmt.Sprintf("api.curseforge.com/v1/mods/%d", i)] = cfProjectData(i, 6, true)
		routes[fmt.Sprintf("api.curseforge.com/v1/mods/%d/files", i)] = jsonBytes(map[string]any{"data": []any{f}})
		routes[fmt.Sprintf("api.curseforge.com/v1/mods/%d/files/%d", i, i*10)] = jsonBytes(map[string]any{"data": f})
		routes[fmt.Sprintf("edge.forgecdn.net/mod%d.jar", i)] = []byte("jar")
	}
	s := testService(t, routes)
	r := modRequest("curseforge")
	r.ProjectID = "1"
	r.VersionID = "10"
	for _, optional := range []bool{false, true} {
		r.IncludeOptional = optional
		p, err := s.Plan(context.Background(), r, "unused", nil)
		if err != nil {
			t.Fatal(err)
		}
		want := 2
		if optional {
			want++
		}
		if len(p.Files) != want {
			t.Fatalf("wrong dependency count: %d", len(p.Files))
		}
		dest := filepath.Join(t.TempDir(), "mod.jar")
		if err = s.client.Download(context.Background(), p.Files[0], dest, nil); err != nil {
			t.Fatal(err)
		}
		routes["edge.forgecdn.net/mod1.jar"] = []byte("bad")
		if err = s.client.Download(context.Background(), p.Files[0], filepath.Join(t.TempDir(), "bad.jar"), nil); err == nil {
			t.Fatal("SHA1 mismatch accepted")
		}
		routes["edge.forgecdn.net/mod1.jar"] = []byte("jar")
	}
}
func TestCFDependencyDepthAndCountLimits(t *testing.T) {
	for _, scenario := range []string{"depth", "count"} {
		t.Run(scenario, func(t *testing.T) {
			routes := map[string][]byte{}
			for i := 1; i <= maxDependencies+1; i++ {
				f := cfFileData(i, i*10, fmt.Sprintf("mod%d.jar", i), []byte("jar"))
				if scenario == "depth" {
					f["dependencies"] = []map[string]any{{"modId": i + 1, "relationType": 3}}
				} else if i == 1 {
					deps := []map[string]any{}
					for j := 2; j <= maxDependencies+1; j++ {
						deps = append(deps, map[string]any{"modId": j, "relationType": 3})
					}
					f["dependencies"] = deps
				}
				routes[fmt.Sprintf("api.curseforge.com/v1/mods/%d", i)] = cfProjectData(i, 6, true)
				routes[fmt.Sprintf("api.curseforge.com/v1/mods/%d/files", i)] = jsonBytes(map[string]any{"data": []any{f}})
			}
			s := testService(t, routes)
			r := modRequest("curseforge")
			r.ProjectID = "1"
			r.VersionID = ""
			if _, err := s.Plan(context.Background(), r, "unused", nil); err == nil || !strings.Contains(err.Error(), "limit") {
				t.Fatalf("unbounded dependencies: %v", err)
			}
		})
	}
}
func TestMRUnpinnedAndVersionOnlyOptionalDependencies(t *testing.T) {
	routes := map[string][]byte{
		"api.modrinth.com/v2/project/a":         mrProjectBytes("a", "required"),
		"api.modrinth.com/v2/project/b":         mrProjectBytes("b", "required"),
		"api.modrinth.com/v2/project/a/version": jsonBytes([]mrVersion{mrMod("a", "v1", mrDependency{VersionID: "v2", Type: "optional"})}),
		"api.modrinth.com/v2/version/v2":        jsonBytes(mrMod("b", "v2", mrDependency{ProjectID: "a", Type: "required"})),
	}
	s := testService(t, routes)
	r := modRequest("modrinth")
	r.VersionID = ""
	for _, optional := range []bool{false, true} {
		r.IncludeOptional = optional
		p, err := s.Plan(context.Background(), r, "unused", nil)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if optional {
			want++
		}
		if len(p.Files) != want {
			t.Fatalf("wrong optional count: %d", len(p.Files))
		}
	}
}
func TestCFManifestDuplicateTraversalAndPinnedConflict(t *testing.T) {
	for _, scenario := range []string{"duplicate", "traversal", "conflict"} {
		t.Run(scenario, func(t *testing.T) {
			files := []map[string]any{{"projectID": 1, "fileID": 10, "required": true}}
			if scenario == "duplicate" {
				files = append(files, map[string]any{"projectID": 1, "fileID": 11, "required": true})
			}
			manifest := manifestData("fabric-0.16.10", files)
			if scenario == "traversal" {
				manifest = []byte(strings.ReplaceAll(string(manifest), `"overrides":"overrides"`, `"overrides":"../outside"`))
				manifest = []byte(strings.ReplaceAll(string(manifest), `"files":[{"fileID":10,"projectID":1,"required":true}]`, `"files":[]`))
			}
			routes := cfPackRoutes(t, manifest, false)
			f := cfFileData(1, 11, "mod.jar", []byte("jar"))
			routes["api.curseforge.com/v1/mods/1"] = cfProjectData(1, 6, true)
			routes["api.curseforge.com/v1/mods/1/files/10"] = jsonBytes(map[string]any{"data": f})
			s := testService(t, routes)
			if _, err := s.Plan(context.Background(), packRequest("curseforge"), "unused", nil); err == nil {
				t.Fatal("invalid CF manifest/file accepted")
			}
		})
	}
}
func TestVanillaPacksAndTargetMismatch(t *testing.T) {
	for _, provider := range []string{"modrinth", "curseforge"} {
		t.Run(provider, func(t *testing.T) {
			routes := map[string][]byte{}
			if provider == "modrinth" {
				routes = mrPackRoutes(archiveFixture(t, zipItem{"modrinth.index.json", indexData(nil, ""), 0}))
			} else {
				routes = cfPackRoutes(t, manifestData("", nil), false)
			}
			s := testService(t, routes)
			r := packRequest(provider)
			p, err := s.Plan(context.Background(), r, "unused", nil)
			if err != nil || p.Loader != "vanilla" || p.LoaderVersion != "" {
				t.Fatalf("vanilla pack failed: %+v %v", p, err)
			}
			// CF checks the authoritative manifest even when target fields are specified.
			if provider == "curseforge" {
				r.GameVersion = "1.20.1"
				if _, err = s.Plan(context.Background(), r, "unused", nil); err == nil {
					t.Fatal("target mismatch accepted")
				}
			}
		})
	}
}
func TestSecretTransportErrorAndCancellation(t *testing.T) {
	s := testService(t, map[string][]byte{})
	s.client.HTTP.Transport = transportFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("test-secret") })
	_, err := s.Search(context.Background(), SearchRequest{Provider: "curseforge", Kind: "mod", Side: "client"})
	if err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("secret transport error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.Search(ctx, SearchRequest{Provider: "curseforge", Kind: "mod", Side: "client"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
func TestAdditionalWindowsDeviceAliases(t *testing.T) {
	for _, name := range []string{"config/COM¹.txt", "config/LPT²", "config/CONIN$", "config/CONOUT$.txt", "config/CON .txt", "launcher.json"} {
		if safeDestination(name) {
			t.Errorf("reserved Windows/control path accepted: %q", name)
		}
	}
}

func TestCFRequiredDependencyPreservesOptionalManifestPin(t *testing.T) {
	files := []map[string]any{{"projectID": 1, "fileID": 10, "required": true}, {"projectID": 2, "fileID": 20, "required": false}}
	routes := cfPackRoutes(t, manifestData("fabric-0.16.10", files), false)
	for i := 1; i <= 2; i++ {
		f := cfFileData(i, i*10, fmt.Sprintf("mod%d.jar", i), []byte("jar"))
		if i == 1 {
			f["dependencies"] = []map[string]any{{"modId": 2, "relationType": 3}}
		}
		routes[fmt.Sprintf("api.curseforge.com/v1/mods/%d", i)] = cfProjectData(i, 6, true)
		routes[fmt.Sprintf("api.curseforge.com/v1/mods/%d/files/%d", i, i*10)] = jsonBytes(map[string]any{"data": f})
	}
	s := testService(t, routes)
	p, err := s.Plan(context.Background(), packRequest("curseforge"), "unused", nil)
	if err != nil || len(p.Files) != 2 {
		t.Fatalf("required optional manifest pin lost: %+v %v", p, err)
	}
}

func TestDependencyEdgeLimits(t *testing.T) {
	deps := make([]mrDependency, 1025)
	for i := range deps {
		deps[i] = mrDependency{VersionID: "v1", Type: "required"}
	}
	routes := map[string][]byte{"api.modrinth.com/v2/project/a": mrProjectBytes("a", "required"), "api.modrinth.com/v2/version/v1": jsonBytes(mrMod("a", "v1", deps...))}
	s := testService(t, routes)
	if _, err := s.Plan(context.Background(), modRequest("modrinth"), "unused", nil); err == nil || !strings.Contains(err.Error(), "edge limit") {
		t.Fatalf("MR edge limit: %v", err)
	}
	f := cfFileData(1, 10, "mod.jar", []byte("jar"))
	cfDeps := make([]map[string]any, 1025)
	for i := range cfDeps {
		cfDeps[i] = map[string]any{"modId": 1, "relationType": 3}
	}
	f["dependencies"] = cfDeps
	s = testService(t, map[string][]byte{"api.curseforge.com/v1/mods/1": cfProjectData(1, 6, true), "api.curseforge.com/v1/mods/1/files/10": jsonBytes(map[string]any{"data": f})})
	r := modRequest("curseforge")
	r.ProjectID = "1"
	r.VersionID = "10"
	if _, err := s.Plan(context.Background(), r, "unused", nil); err == nil || !strings.Contains(err.Error(), "edge limit") {
		t.Fatalf("CF edge limit: %v", err)
	}
}
