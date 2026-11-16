//go:build windows

package app

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Pekly/NeoLauncher/internal/content"
	"github.com/Pekly/NeoLauncher/internal/download"
	"github.com/Pekly/NeoLauncher/internal/launcher"
)

type mockVendor func(*http.Request) (*http.Response, error)

func (f mockVendor) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixtureApp(t *testing.T, jar []byte) (*App, *atomic.Int32) {
	t.Helper()
	a, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.closeServices() })
	hash := func(data []byte) string { return fmt.Sprintf("%x", sha1.Sum(data)) }
	assets := []byte(`{"objects":{}}`)
	artifact := func(url string, data []byte) map[string]any {
		return map[string]any{"url": url, "sha1": hash(data), "size": len(data)}
	}
	metadata, _ := json.Marshal(map[string]any{"id": "1.21.1", "type": "release", "mainClass": "fixture.Main", "javaVersion": map[string]int{"majorVersion": 21}, "downloads": map[string]any{"server": artifact("https://piston-data.mojang.com/server.jar", jar), "client": artifact("https://piston-data.mojang.com/client.jar", jar)}, "libraries": []any{}, "assetIndex": map[string]any{"id": "fixture", "url": "https://resources.download.minecraft.net/index.json", "sha1": hash(assets), "size": len(assets)}, "arguments": map[string]any{"jvm": []string{"-Djava.library.path=${natives_directory}", "-cp", "${classpath}"}, "game": []string{"--username", "${auth_player_name}"}}})
	manifest, _ := json.Marshal(map[string]any{"versions": []any{map[string]any{"id": "1.21.1", "type": "release", "url": "https://piston-meta.mojang.com/version.json", "sha1": hash(metadata)}}})
	files := map[string][]byte{"https://piston-meta.mojang.com/mc/game/version_manifest_v2.json": manifest, "https://piston-meta.mojang.com/version.json": metadata, "https://piston-data.mojang.com/server.jar": jar, "https://piston-data.mojang.com/client.jar": jar, "https://resources.download.minecraft.net/index.json": assets}
	calls := &atomic.Int32{}
	a.downloads.HTTP = &http.Client{Transport: mockVendor(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		data, ok := files[r.URL.String()]
		status := 200
		if !ok {
			status = 404
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Header: http.Header{}, Request: r}, nil
	})}
	return a, calls
}
func waitOperation(t *testing.T, a *App, id string) Operation {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, op := range a.GetState().Operations {
			if op.ID == id && op.State != "running" {
				return op
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("operation did not finish")
	return Operation{}
}
func TestServerInstallEULAGuardAndCachedRetry(t *testing.T) {
	a, calls := fixtureApp(t, []byte("fixture jar not executed"))
	state, err := a.CreateServer(launcher.CreateServerRequest{Name: "Test", Version: "1.21.1", Port: 25565, MemoryMB: 512})
	if err != nil {
		t.Fatal(err)
	}
	id := state.Servers[0].ID
	op, err := a.InstallProfile("server", id)
	if err != nil {
		t.Fatal(err)
	}
	done := waitOperation(t, a, op.ID)
	if done.State != "complete" {
		t.Fatal(done.Error)
	}
	profile, err := a.store.GetProfile("server", id)
	if err != nil || !profile.Installed {
		t.Fatal("installation not persisted", err)
	}
	if _, err = a.StartServer(id, false); err == nil {
		t.Fatal("server started without EULA consent")
	}
	eula, _ := os.ReadFile(filepath.Join(profile.Directory, "eula.txt"))
	if !strings.Contains(string(eula), "eula=false") {
		t.Fatal("EULA changed without consent")
	}
	before := calls.Load()
	if _, err = a.installBase(context.Background(), profile, nil); err != nil {
		t.Fatal("verified base cannot be reused", err)
	}
	if calls.Load() != before {
		t.Fatal("retry downloaded the installed base again")
	}
	if _, err = a.SetProfileVersion("server", id, "1.20.1", "vanilla"); err == nil {
		t.Fatal("installed profile silently changed version")
	}
}
func TestClientInstallRequiresAccountToLaunch(t *testing.T) {
	a, _ := fixtureApp(t, []byte("fixture client jar not executed"))
	state, err := a.CreateInstance(launcher.CreateInstanceRequest{Name: "Client", Version: "1.21.1", Loader: "vanilla"})
	if err != nil {
		t.Fatal(err)
	}
	id := state.Instances[0].ID
	op, err := a.InstallProfile("client", id)
	if err != nil {
		t.Fatal(err)
	}
	done := waitOperation(t, a, op.ID)
	if done.State != "complete" {
		t.Fatal(done.Error)
	}
	if _, err = a.StartClient(id); err == nil {
		t.Fatal("client launched without licensed account")
	}
	if a.runner.ActiveCount() != 0 {
		t.Fatal("unauthenticated process started")
	}
}
func TestContentPublicationPreservesUserFiles(t *testing.T) {
	a, _ := fixtureApp(t, []byte("fixture"))
	state, err := a.CreateInstance(launcher.CreateInstanceRequest{Name: "Content", Version: "1.21.1", Loader: "fabric"})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := a.store.GetProfile("client", state.Instances[0].ID)
	work, err := a.workDirectory()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(work)
	plan := &content.Plan{Overrides: []content.Override{{Path: "config/test.txt", Data: []byte("original")}}}
	if err = a.publishContent(context.Background(), profile, plan, work, nil); err != nil {
		t.Fatal(err)
	}
	if err = a.publishContent(context.Background(), profile, plan, work, nil); err != nil {
		t.Fatal("identical override not idempotent", err)
	}
	plan.Overrides[0].Data = []byte("changed")
	if err = a.publishContent(context.Background(), profile, plan, work, nil); err == nil {
		t.Fatal("user content overwritten")
	}
	existing, _ := os.ReadFile(filepath.Join(profile.Directory, "config", "test.txt"))
	if string(existing) != "original" {
		t.Fatal("original changed")
	}
	plan.Overrides = []content.Override{{Path: "../outside", Data: []byte("bad")}}
	if err = a.publishContent(context.Background(), profile, plan, work, nil); err == nil {
		t.Fatal("traversal accepted")
	}
}
func TestOperationsCancelAndReleaseProfile(t *testing.T) {
	a, _ := fixtureApp(t, []byte("fixture"))
	started := make(chan struct{})
	op, err := a.startOperation("client", "target", "test", func(ctx context.Context, _ *operation) error { close(started); <-ctx.Done(); return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err = a.startOperation("client", "target", "duplicate", func(context.Context, *operation) error { return nil }); err == nil {
		t.Fatal("duplicate operation allowed")
	}
	a.CancelOperation(op.ID)
	if result := waitOperation(t, a, op.ID); result.State != "cancelled" {
		t.Fatal(result)
	}
	op, err = a.startOperation("client", "target", "panic", func(context.Context, *operation) error { panic("secret-not-logged") })
	if err != nil {
		t.Fatal(err)
	}
	result := waitOperation(t, a, op.ID)
	if result.State != "failed" || strings.Contains(result.Error, "secret-not-logged") {
		t.Fatal(result)
	}
}
func TestCurseForgeKeyNeverAppearsInState(t *testing.T) {
	a, _ := fixtureApp(t, []byte("fixture"))
	settings := a.GetState().Settings
	settings.CurseForgeEnabled = true
	if _, err := a.SaveSettings(settings); err == nil {
		t.Fatal("enabled provider without key")
	}
	const key = "fixture-secret-key-987654321"
	state, err := a.SetCurseForgeKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if !state.CurseForgeConfigured || state.Settings.CurseForgeEnabled {
		t.Fatal("provider enabled automatically")
	}
	state, err = a.SaveSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(state)
	onDisk, _ := os.ReadFile(filepath.Join(a.root, "state.json"))
	if bytes.Contains(encoded, []byte(key)) || bytes.Contains(onDisk, []byte(key)) {
		t.Fatal("secret exposed in UI or settings")
	}
	state, err = a.SetCurseForgeKey("")
	if err != nil {
		t.Fatal(err)
	}
	if state.CurseForgeConfigured || state.Settings.CurseForgeEnabled {
		t.Fatal("secret removal left provider enabled")
	}
}

func TestJavaServerFixtureEndToEnd(t *testing.T) {
	java, jarPath := os.Getenv("NEOLAUNCHER_TEST_JAVA"), os.Getenv("NEOLAUNCHER_TEST_SERVER_JAR")
	if java == "" || jarPath == "" {
		t.Skip("set isolated Java fixture paths to run the native integration")
	}
	jar, err := os.ReadFile(jarPath)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := fixtureApp(t, jar)
	settings := a.GetState().Settings
	settings.JavaPath = java
	if _, err = a.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	state, err := a.CreateServer(launcher.CreateServerRequest{Name: "Synthetic Java fixture", Version: "1.21.1", Port: port, MemoryMB: 512})
	if err != nil {
		t.Fatal(err)
	}
	id := state.Servers[0].ID
	op, err := a.InstallProfile("server", id)
	if err != nil {
		t.Fatal(err)
	}
	if result := waitOperation(t, a, op.ID); result.State != "complete" {
		t.Fatal(result.Error)
	}
	// This tests explicit consent handling using a synthetic fixture, not Minecraft.
	if _, err = a.StartServer(id, true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		for _, p := range a.GetState().Processes {
			if p.ID == id && p.Status == "running" {
				ready = true
			}
		}
		if ready {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !ready {
		t.Fatal("fixture not ready", a.GetLogs("server", id))
	}
	if err = a.SendServerCommand(id, "list"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.StopServer(id); err != nil {
		t.Fatal(err)
	}
	profile, _ := a.store.GetProfile("server", id)
	saved, err := os.ReadFile(filepath.Join(profile.Directory, "clean-stop.txt"))
	if err != nil || string(saved) != "saved" {
		t.Fatal("fixture did not save", err)
	}
	if a.runner.ActiveCount() != 0 {
		t.Fatal("fixture process survived stop")
	}
	sum := sha256.Sum256(jar)
	t.Logf("Verified synthetic fixture digest %x; no real world or Minecraft EULA used", sum[:4])
}

func TestLiveVendorDownloads(t *testing.T) {
	if os.Getenv("NEOLAUNCHER_LIVE_INSTALL") != "1" {
		t.Skip("opt-in live vendor download verification")
	}
	a, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.closeServices()
	for index, loader := range []string{"vanilla", "fabric"} {
		state, err := a.CreateServer(launcher.CreateServerRequest{Name: "Download verification " + loader, Version: "1.21.1", Loader: loader, Port: 25565 + index, MemoryMB: 512})
		if err != nil {
			t.Fatal(err)
		}
		id := state.Servers[len(state.Servers)-1].ID
		op, err := a.InstallProfile("server", id)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Minute)
		complete := false
		for time.Now().Before(deadline) {
			for _, result := range a.GetState().Operations {
				if result.ID == op.ID && result.State != "running" {
					if result.State != "complete" {
						t.Fatal(result.Error)
					}
					complete = true
				}
			}
			if complete {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !complete {
			t.Fatal("live download timed out")
		}
		if _, err = a.StartServer(id, false); err == nil {
			t.Fatal("real server started without EULA consent")
		}
		profile, _ := a.store.GetProfile("server", id)
		eula, _ := os.ReadFile(filepath.Join(profile.Directory, "eula.txt"))
		if !strings.Contains(string(eula), "eula=false") {
			t.Fatal("real EULA was modified")
		}
		t.Log("Verified stopped Minecraft 1.21.1 server installation:", loader)
	}
	projects, err := a.SearchContent(content.SearchRequest{Provider: "modrinth", Kind: "mod", Query: "Fabric API", GameVersion: "1.21.1", Loader: "fabric", Side: "server"})
	if err != nil || len(projects) == 0 {
		t.Fatal("live Modrinth search", err)
	}
	project := projects[0]
	for _, p := range projects {
		if p.Name == "Fabric API" {
			project = p
		}
	}
	versions, err := a.GetContentVersions(ContentVersionsRequest{Provider: "modrinth", ProjectID: project.ID, GameVersion: "1.21.1", Loader: "fabric"})
	if err != nil || len(versions) == 0 {
		t.Fatal("live Modrinth versions", err)
	}
	work, err := a.workDirectory()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(work)
	plan, err := a.content.Plan(a.life, content.InstallRequest{Provider: "modrinth", Kind: "mod", ProjectID: project.ID, VersionID: versions[0].ID, GameVersion: "1.21.1", Loader: "fabric", Side: "server"}, work, nil)
	if err != nil || len(plan.Files) == 0 {
		t.Fatal("live Modrinth plan", err)
	}
	file := plan.Files[0]
	dest, err := download.SafeJoin(work, file.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.downloads.Download(a.life, file, dest, nil); err != nil {
		t.Fatal(err)
	}
	t.Log("Verified Modrinth download checksum:", project.Name)
}
