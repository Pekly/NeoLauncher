package content

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Pekly/NeoLauncher/internal/download"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body []byte) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}
}
func jsonBytes(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
func testService(t *testing.T, routes map[string][]byte) *Service {
	t.Helper()
	c := download.New()
	c.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "api.curseforge.com" {
			if r.Header.Get("x-api-key") != "test-secret" {
				t.Error("missing CF key")
			}
		} else if r.Header.Get("x-api-key") != "" {
			t.Error("credential leaked to download/non-CF API")
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected credentials")
		}
		key := r.URL.Hostname() + r.URL.Path
		data, ok := routes[key]
		if !ok {
			t.Errorf("unexpected mock request: %s", key)
			return nil, errors.New("unexpected request")
		}
		return response(data), nil
	})
	return New(c, func() (string, error) { return "test-secret", nil }, func() bool { return true })
}

type zipItem struct {
	name string
	data []byte
	mode os.FileMode
}

func archiveFixture(t *testing.T, entries ...zipItem) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, entry := range entries {
		h := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			h.SetMode(entry.mode)
		}
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func sha1Hex(data []byte) string { h := sha1.Sum(data); return hex.EncodeToString(h[:]) }
func mrMod(project, version string, deps ...mrDependency) mrVersion {
	return mrVersion{ID: version, ProjectID: project, Name: version, GameVersions: []string{"1.21.1"}, Loaders: []string{"fabric"}, Files: []mrFile{{URL: "https://cdn.modrinth.com/" + project + ".jar", Filename: project + ".jar", Size: 3, Hashes: map[string]string{"sha1": sha1Hex([]byte("jar"))}}}, Dependencies: deps}
}
func modRequest(provider string) InstallRequest {
	return InstallRequest{Provider: provider, Kind: "mod", ProjectID: "a", VersionID: "v1", GameVersion: "1.21.1", Loader: "fabric", Side: "client"}
}
func mrProjectBytes(id, side string) []byte {
	return jsonBytes(mrProject{ID: id, Title: id, ProjectType: "mod", ClientSide: "required", ServerSide: side})
}
func TestModrinthRequiredDependenciesCycleAndSides(t *testing.T) {
	routes := map[string][]byte{
		"api.modrinth.com/v2/project/a":  mrProjectBytes("a", "required"),
		"api.modrinth.com/v2/project/b":  mrProjectBytes("b", "unsupported"),
		"api.modrinth.com/v2/version/v1": jsonBytes(mrMod("a", "v1", mrDependency{ProjectID: "b", VersionID: "v2", Type: "required"}, mrDependency{ProjectID: "notrequested", Type: "optional"})),
		"api.modrinth.com/v2/version/v2": jsonBytes(mrMod("b", "v2", mrDependency{ProjectID: "a", VersionID: "v1", Type: "required"})),
	}
	s := testService(t, routes)
	r := modRequest("modrinth")
	p, err := s.Plan(context.Background(), r, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 2 || p.Version != "1.21.1" || p.Loader != "fabric" {
		t.Fatalf("bad plan: %+v", p)
	}
	for _, f := range p.Files {
		if f.AllowOverwrite || f.AllowUnverified || f.SHA1 == "" {
			t.Fatal("unsafe file policy")
		}
	}
	r.Side = "server"
	if _, err = s.Plan(context.Background(), r, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "side") {
		t.Fatalf("expected side incompatibility: %v", err)
	}
	r.Side = "client"
	r.GameVersion = "1.20.1"
	if _, err = s.Plan(context.Background(), r, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("expected version incompatibility: %v", err)
	}
}
func TestModrinthDependencyBoundsAndConflicts(t *testing.T) {
	for _, scenario := range []string{"depth", "conflict"} {
		t.Run(scenario, func(t *testing.T) {
			routes := map[string][]byte{}
			for i := 0; i <= maxDepth+1; i++ {
				id := fmt.Sprintf("p%d", i)
				v := fmt.Sprintf("v%d", i)
				dep := mrDependency{ProjectID: fmt.Sprintf("p%d", i+1), VersionID: fmt.Sprintf("v%d", i+1), Type: "required"}
				if scenario == "conflict" {
					dep = mrDependency{ProjectID: id, VersionID: "different", Type: "required"}
				}
				routes["api.modrinth.com/v2/project/"+id] = mrProjectBytes(id, "required")
				routes["api.modrinth.com/v2/version/"+v] = jsonBytes(mrMod(id, v, dep))
			}
			s := testService(t, routes)
			r := modRequest("modrinth")
			r.ProjectID = "p0"
			r.VersionID = "v0"
			_, err := s.Plan(context.Background(), r, "unused", nil)
			if err == nil || !(strings.Contains(err.Error(), "limit") || strings.Contains(err.Error(), "conflicting")) {
				t.Fatalf("expected bounded failure: %v", err)
			}
		})
	}
}
func mrPackRoutes(data []byte) map[string][]byte {
	h := sha512.Sum512(data)
	v := mrVersion{ID: "packv", ProjectID: "pack", Name: "Pack", Files: []mrFile{{URL: "https://cdn.modrinth.com/pack.mrpack", Filename: "pack.mrpack", Size: int64(len(data)), Hashes: map[string]string{"sha512": hex.EncodeToString(h[:])}}}}
	return map[string][]byte{"api.modrinth.com/v2/project/pack": jsonBytes(mrProject{ID: "pack", Title: "Pack", ProjectType: "modpack"}), "api.modrinth.com/v2/version/packv": jsonBytes(v), "cdn.modrinth.com/pack.mrpack": data}
}
func packRequest(provider string) InstallRequest {
	r := InstallRequest{Provider: provider, Kind: "modpack", ProjectID: "pack", VersionID: "packv", Side: "client"}
	if provider == "curseforge" {
		r.ProjectID = "100"
		r.VersionID = "200"
	}
	return r
}
func indexFile(name, client, server string) map[string]any {
	return map[string]any{"path": name, "hashes": map[string]string{"sha1": sha1Hex([]byte("jar"))}, "env": map[string]string{"client": client, "server": server}, "downloads": []string{"https://cdn.modrinth.com/file.jar"}, "fileSize": 3}
}
func indexData(files []map[string]any, loader string) []byte {
	deps := map[string]string{"minecraft": "1.21.1"}
	if loader != "" {
		deps[loader] = "0.16.10"
	}
	return jsonBytes(map[string]any{"formatVersion": 1, "game": "minecraft", "name": "Fixture Pack", "dependencies": deps, "files": files})
}
func TestMRPackSidesOptionalsAndProtectedServerSettings(t *testing.T) {
	data := archiveFixture(t,
		zipItem{"modrinth.index.json", indexData([]map[string]any{indexFile("mods/common.jar", "required", "required"), indexFile("mods/client.jar", "required", "unsupported"), indexFile("mods/optional.jar", "optional", "optional")}, "fabric-loader"), 0},
		zipItem{"overrides/config/common.json", []byte("{}"), 0},
		zipItem{"client-overrides/config/client.json", []byte("{}"), 0},
		zipItem{"server-overrides/config/server.json", []byte("{}"), 0},
		zipItem{"server-overrides/eula.txt", []byte("eula=true"), 0},
		zipItem{"overrides/server.properties", []byte("online-mode=false"), 0})
	s := testService(t, mrPackRoutes(data))
	for _, side := range []string{"client", "server"} {
		for _, optional := range []bool{false, true} {
			r := packRequest("modrinth")
			r.Side = side
			r.IncludeOptional = optional
			p, err := s.Plan(context.Background(), r, t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if side == "server" {
				want = 1
			}
			if optional {
				want++
			}
			if len(p.Files) != want || p.LoaderVersion != "0.16.10" || p.Version != "1.21.1" {
				t.Fatalf("bad pack: %+v", p)
			}
			for _, o := range p.Overrides {
				if side == "server" && (strings.Contains(o.Path, "client") || skipServer(o.Path, side)) {
					t.Fatalf("client/protected override on server: %s", o.Path)
				}
				if side == "client" && strings.Contains(o.Path, "config/server") {
					t.Fatal("server override on client")
				}
			}
			if side == "server" && (len(p.Overrides) != 2 || len(p.Warnings) == 0) {
				t.Fatal("server overrides/settings protection missing")
			}
		}
	}
}
func TestMRPackRejectUnsupportedLoadersAndDestinations(t *testing.T) {
	for _, loader := range []string{"forge", "neoforge", "quilt-loader"} {
		t.Run(loader, func(t *testing.T) {
			a := archiveFixture(t, zipItem{"modrinth.index.json", indexData(nil, loader), 0})
			s := testService(t, mrPackRoutes(a))
			if _, err := s.Plan(context.Background(), packRequest("modrinth"), "unused", nil); err == nil || !strings.Contains(err.Error(), "unsupported loader") {
				t.Fatalf("expected loader rejection: %v", err)
			}
		})
	}
	cases := map[string][]zipItem{
		"traversal":          {{"overrides/../evil", []byte("x"), 0}},
		"windows":            {{"overrides/config/CON.json", []byte("x"), 0}},
		"metadata":           {{"overrides/.neolauncher-state.json", []byte("x"), 0}},
		"launcher":           {{"overrides/launcher_profiles.json", []byte("x"), 0}},
		"symlink":            {{"overrides/config/link", []byte("outside"), os.ModeSymlink | 0777}},
		"duplicate-archive":  {{"overrides/config/a", []byte("a"), 0}, {"overrides/config/A", []byte("b"), 0}},
		"duplicate-selected": {{"overrides/config/a", []byte("a"), 0}, {"client-overrides/config/A", []byte("b"), 0}},
		"file-directory":     {{"overrides/config", []byte("a"), 0}, {"overrides/config/a", []byte("b"), 0}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			entries = append(entries, zipItem{"modrinth.index.json", indexData(nil, "fabric-loader"), 0})
			s := testService(t, mrPackRoutes(archiveFixture(t, entries...)))
			if _, err := s.Plan(context.Background(), packRequest("modrinth"), "unused", nil); err == nil {
				t.Fatal("unsafe pack accepted")
			}
		})
	}
}
func TestMRPackIndexDuplicatesAndURLHashes(t *testing.T) {
	for _, scenario := range []string{"duplicate", "override-conflict", "bad-url", "missing-hash", "traversal", "invalid-env"} {
		t.Run(scenario, func(t *testing.T) {
			item := indexFile("mods/a.jar", "required", "required")
			items := []map[string]any{item}
			entries := []zipItem{}
			switch scenario {
			case "duplicate":
				items = append(items, indexFile("mods/A.jar", "required", "required"))
			case "override-conflict":
				entries = append(entries, zipItem{"overrides/mods/A.jar", []byte("x"), 0})
			case "bad-url":
				item["downloads"] = []string{"https://evil.example/file.jar"}
			case "missing-hash":
				delete(item, "hashes")
			case "traversal":
				item["path"] = "../outside"
			case "invalid-env":
				item["env"] = map[string]string{"server": "maybe"}
			}
			entries = append(entries, zipItem{"modrinth.index.json", indexData(items, "fabric-loader"), 0})
			s := testService(t, mrPackRoutes(archiveFixture(t, entries...)))
			if _, err := s.Plan(context.Background(), packRequest("modrinth"), "unused", nil); err == nil {
				t.Fatal("invalid index accepted")
			}
		})
	}
}
func TestArchiveLimitsChecksumsAndSizeMismatch(t *testing.T) {
	t.Run("entry-limit", func(t *testing.T) {
		data := archiveFixture(t, zipItem{"overrides/huge", bytes.Repeat([]byte("a"), int(maxEntryBytes)+1), 0})
		if _, err := readArchive(context.Background(), data); err == nil {
			t.Fatal("oversize entry accepted")
		}
	})
	t.Run("total-limit", func(t *testing.T) {
		entries := []zipItem{}
		for i := 0; i < 5; i++ {
			entries = append(entries, zipItem{fmt.Sprintf("overrides/%d", i), bytes.Repeat([]byte("a"), int(maxEntryBytes)), 0})
		}
		if _, err := readArchive(context.Background(), archiveFixture(t, entries...)); err == nil {
			t.Fatal("oversize total accepted")
		}
	})
	t.Run("entry-count", func(t *testing.T) {
		entries := []zipItem{}
		for i := 0; i <= maxEntries; i++ {
			entries = append(entries, zipItem{fmt.Sprintf("overrides/%d", i), nil, 0})
		}
		if _, err := readArchive(context.Background(), archiveFixture(t, entries...)); err == nil {
			t.Fatal("too many entries accepted")
		}
	})
	t.Run("zipbomb-size-mismatch", func(t *testing.T) {
		data := archiveFixture(t, zipItem{"overrides/a", bytes.Repeat([]byte("x"), 1024), 0})
		pos := bytes.Index(data, []byte{'P', 'K', 1, 2})
		if pos < 0 {
			t.Fatal("no central directory")
		}
		binary.LittleEndian.PutUint32(data[pos+24:pos+28], 1)
		if _, err := readArchive(context.Background(), data); err == nil {
			t.Fatal("forged expanded size accepted")
		}
	})
	t.Run("published-hash", func(t *testing.T) {
		data := archiveFixture(t, zipItem{"modrinth.index.json", indexData(nil, ""), 0})
		routes := mrPackRoutes(data)
		routes["cdn.modrinth.com/pack.mrpack"] = append([]byte(nil), data...)
		routes["cdn.modrinth.com/pack.mrpack"][0] ^= 1
		s := testService(t, routes)
		if _, err := s.Plan(context.Background(), packRequest("modrinth"), "unused", nil); err == nil || !strings.Contains(err.Error(), "checksum") {
			t.Fatalf("expected hash failure: %v", err)
		}
	})
	t.Run("compressed-metadata-limit", func(t *testing.T) {
		s := testService(t, map[string][]byte{})
		_, err := s.fetchArchive(context.Background(), download.File{Path: "packs/x.zip", URL: "https://cdn.modrinth.com/x.zip", SHA1: sha1Hex(nil), Size: maxArchiveBytes + 1}, nil)
		if err == nil {
			t.Fatal("oversize compressed metadata accepted")
		}
	})
}
func cfProjectData(id int, class int, allowed bool) []byte {
	return jsonBytes(map[string]any{"data": map[string]any{"id": id, "gameId": 432, "classId": class, "name": "CF Fixture", "allowModDistribution": allowed}})
}
func cfFileData(project, id int, name string, data []byte) map[string]any {
	return map[string]any{"id": id, "modId": project, "displayName": name, "fileName": name, "downloadUrl": "https://edge.forgecdn.net/" + name, "fileLength": len(data), "gameVersions": []string{"1.21.1", "Fabric"}, "isAvailable": true, "hashes": []map[string]any{{"algo": 1, "value": sha1Hex(data)}}}
}
func manifestData(loader string, files []map[string]any) []byte {
	loaders := []map[string]any{}
	if loader != "" {
		loaders = append(loaders, map[string]any{"id": loader, "primary": true})
	}
	return jsonBytes(map[string]any{"manifestType": "minecraftModpack", "manifestVersion": 1, "name": "CF Pack", "minecraft": map[string]any{"version": "1.21.1", "modLoaders": loaders}, "files": files, "overrides": "overrides"})
}
func cfPackRoutes(t *testing.T, manifest []byte, server bool) map[string][]byte {
	data := archiveFixture(t, zipItem{"manifest.json", manifest, 0}, zipItem{"overrides/config/a.json", []byte("{}"), 0}, zipItem{"overrides/eula.txt", []byte("eula=true"), 0}, zipItem{"overrides/server.properties", []byte("online-mode=false"), 0})
	f := cfFileData(100, 200, "pack.zip", data)
	routes := map[string][]byte{"api.curseforge.com/v1/mods/100": cfProjectData(100, 4471, true), "edge.forgecdn.net/pack.zip": data}
	if server {
		f["serverPackFileId"] = 201
		routes["api.curseforge.com/v1/mods/100/files/201"] = jsonBytes(map[string]any{"data": cfFileData(100, 201, "server.zip", data)})
		routes["edge.forgecdn.net/server.zip"] = data
	}
	routes["api.curseforge.com/v1/mods/100/files/200"] = jsonBytes(map[string]any{"data": f})
	return routes
}
func TestCFManifestPinnedRequiredDependenciesAndOptional(t *testing.T) {
	files := []map[string]any{{"projectID": 1, "fileID": 10, "required": true}, {"projectID": 2, "fileID": 20, "required": true}, {"projectID": 3, "fileID": 30, "required": false}}
	routes := cfPackRoutes(t, manifestData("fabric-0.16.10", files), false)
	for i := 1; i <= 3; i++ {
		routes[fmt.Sprintf("api.curseforge.com/v1/mods/%d", i)] = cfProjectData(i, 6, true)
		f := cfFileData(i, i*10, fmt.Sprintf("mod%d.jar", i), []byte("jar"))
		if i == 1 {
			f["dependencies"] = []map[string]any{{"modId": 2, "relationType": 3}}
		}
		if i == 2 {
			f["dependencies"] = []map[string]any{{"modId": 1, "relationType": 3}}
		}
		routes[fmt.Sprintf("api.curseforge.com/v1/mods/%d/files/%d", i, i*10)] = jsonBytes(map[string]any{"data": f})
	}
	s := testService(t, routes)
	r := packRequest("curseforge")
	for _, optional := range []bool{false, true} {
		r.IncludeOptional = optional
		p, err := s.Plan(context.Background(), r, t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		want := 2
		if optional {
			want++
		}
		if len(p.Files) != want || p.LoaderVersion != "0.16.10" || p.Version != "1.21.1" {
			t.Fatalf("bad CF plan: %+v", p)
		}
	}
}
func TestCFServerPacksRequireOfficialFileAndManifest(t *testing.T) {
	r := packRequest("curseforge")
	r.Side = "server"
	s := testService(t, cfPackRoutes(t, manifestData("fabric-0.16.10", nil), false))
	if _, err := s.Plan(context.Background(), r, "unused", nil); !errors.Is(err, errCFServer) {
		t.Fatalf("client pack accepted as server: %v", err)
	}
	routes := cfPackRoutes(t, manifestData("fabric-0.16.10", nil), true)
	delete(routes, "edge.forgecdn.net/pack.zip") // Client archive must never be requested.
	s = testService(t, routes)
	p, err := s.Plan(context.Background(), r, "unused", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Overrides) != 1 || len(p.Warnings) == 0 {
		t.Fatalf("server settings protection: %+v", p)
	}
	s = testService(t, cfPackRoutes(t, []byte(`{}`), true))
	if _, err = s.Plan(context.Background(), r, "unused", nil); !errors.Is(err, errCFServer) {
		t.Fatalf("unusable server manifest accepted: %v", err)
	}
}
func TestCFUnsupportedLoaders(t *testing.T) {
	for _, loader := range []string{"forge-47.1", "neoforge-21.1", "quilt-0.2"} {
		t.Run(loader, func(t *testing.T) {
			s := testService(t, cfPackRoutes(t, manifestData(loader, nil), false))
			if _, err := s.Plan(context.Background(), packRequest("curseforge"), "unused", nil); err == nil || !strings.Contains(err.Error(), "unsupported loader") {
				t.Fatalf("unsupported pack accepted: %v", err)
			}
		})
	}
}
func TestCFDisabledMissingKeyAndSecretErrors(t *testing.T) {
	r := SearchRequest{Provider: "curseforge", Kind: "mod", Side: "client"}
	for _, tc := range []struct {
		name    string
		enabled func() bool
		key     func() (string, error)
		want    string
	}{
		{"default-disabled", nil, func() (string, error) { t.Fatal("key called while disabled"); return "", nil }, "disabled"},
		{"disabled", func() bool { return false }, nil, "disabled"},
		{"missing", func() bool { return true }, nil, "missing"},
		{"blank", func() bool { return true }, func() (string, error) { return " ", nil }, "missing"},
		{"key-error", func() bool { return true }, func() (string, error) { return "", errors.New("test-secret") }, "could not be loaded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(testService(t, map[string][]byte{}).client, tc.key, tc.enabled)
			_, err := s.Search(context.Background(), r)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "test-secret") {
				t.Fatalf("unsafe/wrong CF gate error: %v", err)
			}
		})
	}
	t.Run("redirect-key-scope", func(t *testing.T) {
		c := download.New()
		calls := 0
		c.HTTP.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.URL.Hostname() != "api.curseforge.com" {
				t.Fatal("CF API redirect followed")
			}
			if req.Header.Get("x-api-key") != "test-secret" {
				t.Fatal("missing API key")
			}
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://cdn.modrinth.com/leak"}}, Body: io.NopCloser(strings.NewReader("test-secret"))}, nil
		})
		s := New(c, func() (string, error) { return "test-secret", nil }, func() bool { return true })
		_, err := s.Search(context.Background(), r)
		if err == nil || strings.Contains(err.Error(), "test-secret") || calls != 1 {
			t.Fatalf("redirect secrecy: %v (%d calls)", err, calls)
		}
	})
}
func TestCFAuthorBlockedAndMissingHash(t *testing.T) {
	for _, scenario := range []string{"author-disabled", "missing-url", "unavailable", "missing-hash", "wrong-loader", "wrong-version"} {
		t.Run(scenario, func(t *testing.T) {
			f := cfFileData(1, 10, "mod.jar", []byte("jar"))
			allowed := true
			switch scenario {
			case "author-disabled":
				allowed = false
			case "missing-url":
				f["downloadUrl"] = nil
			case "unavailable":
				f["isAvailable"] = false
			case "missing-hash":
				delete(f, "hashes")
			case "wrong-loader":
				f["gameVersions"] = []string{"1.21.1", "Forge"}
			case "wrong-version":
				f["gameVersions"] = []string{"1.20.1", "Fabric"}
			}
			s := testService(t, map[string][]byte{"api.curseforge.com/v1/mods/1": cfProjectData(1, 6, allowed), "api.curseforge.com/v1/mods/1/files/10": jsonBytes(map[string]any{"data": f})})
			r := modRequest("curseforge")
			r.ProjectID = "1"
			r.VersionID = "10"
			if _, err := s.Plan(context.Background(), r, "unused", nil); err == nil {
				t.Fatal("blocked/invalid CF mod accepted")
			}
		})
	}
}
func TestSearchVersionsAndSafeDTOs(t *testing.T) {
	c := download.New()
	c.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/v2/search":
			if q.Get("query") != "a & b" || !strings.Contains(q.Get("facets"), "server_side:required") {
				t.Fatal("MR filters missing")
			}
			return response([]byte(`{"hits":[{"project_id":"a","title":"A","project_type":"mod","description":"test","icon_url":"https://cdn.modrinth.com/a.png","download_url":"secret"}]}`)), nil
		case "/v2/project/a/version":
			if q.Get("game_versions") != `["1.21.1"]` || q.Get("loaders") != `["fabric"]` {
				t.Fatal("MR version filters missing")
			}
			return response(jsonBytes([]mrVersion{mrMod("a", "v1")})), nil
		case "/v1/mods/search":
			if r.Header.Get("x-api-key") != "test-secret" || q.Get("gameId") != "432" || q.Get("classId") != "4471" || q.Get("modLoaderType") != "4" {
				t.Fatal("CF filters/key missing")
			}
			return response([]byte(`{"data":[{"id":1,"name":"A","gameId":432,"classId":4471}]}`)), nil
		case "/v1/mods/1/files":
			if q.Get("modLoaderType") != "4" || q.Get("gameVersion") != "1.21.1" {
				t.Fatal("CF version filters missing")
			}
			return response(jsonBytes(map[string]any{"data": []any{cfFileData(1, 10, "mod.jar", []byte("jar"))}})), nil
		}
		t.Fatalf("unexpected API path: %s", r.URL.Path)
		return nil, nil
	})
	s := New(c, func() (string, error) { return "test-secret", nil }, func() bool { return true })
	projects, err := s.Search(context.Background(), SearchRequest{Provider: "modrinth", Kind: "mod", Query: "a & b", GameVersion: "1.21.1", Loader: "fabric", Side: "server"})
	if err != nil || len(projects) != 1 {
		t.Fatalf("MR search: %v", err)
	}
	encoded := string(jsonBytes(projects))
	if strings.Contains(encoded, "secret") || !strings.Contains(encoded, `"provider":"modrinth"`) {
		t.Fatal("unsafe project DTO")
	}
	versions, err := s.Versions(context.Background(), "modrinth", "a", "1.21.1", "fabric")
	if err != nil || len(versions) != 1 || versions[0].ID != "v1" {
		t.Fatalf("MR versions: %v", err)
	}
	projects, err = s.Search(context.Background(), SearchRequest{Provider: "curseforge", Kind: "modpack", Loader: "fabric", Side: "client"})
	if err != nil || len(projects) != 1 {
		t.Fatalf("CF search: %v", err)
	}
	versions, err = s.Versions(context.Background(), "curseforge", "1", "1.21.1", "fabric")
	if err != nil || len(versions) != 1 || versions[0].ID != "10" {
		t.Fatalf("CF versions: %v", err)
	}
	if strings.Contains(string(jsonBytes(Override{Path: "x", Data: []byte("private")})), "Data") {
		t.Fatal("override data exposed as JSON")
	}
}
func TestRequestsAndSafeDestinations(t *testing.T) {
	for _, name := range []string{"../evil", "a\\b", "C:/x", "/x", "config/NUL.txt", "config/com1.txt", "config/a.", "config/a ", "config/a:b", "config/a?b", ".neolauncher-lock", "config/.neolauncher.json", "launcher_accounts.json", "versions/x.json", "start.bat", "server.jar"} {
		if safeDestination(name) {
			t.Errorf("accepted unsafe path %q", name)
		}
	}
	for _, name := range []string{"mods/a.jar", "config/a.json", "resourcepacks/a.zip", "eula.txt", "server.properties"} {
		if !safeDestination(name) {
			t.Errorf("rejected valid path %q", name)
		}
	}
	s := testService(t, map[string][]byte{})
	for _, r := range []InstallRequest{{Provider: "other", Kind: "mod", Side: "client"}, {Provider: "modrinth", Kind: "mod", ProjectID: "a", Side: "client"}, {Provider: "modrinth", Kind: "mod", ProjectID: "a", Side: "client", Loader: "forge"}, {Provider: "modrinth", Kind: "modpack", ProjectID: "../x", Side: "client"}} {
		if _, err := s.Plan(context.Background(), r, "unused", nil); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
}
