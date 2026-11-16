package minecraft

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Pekly/NeoLauncher/internal/download"
)

func sha(b []byte) string { h := sha1.Sum(b); return hex.EncodeToString(h[:]) }
func encoded(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
func rawArgs(args ...string) []json.RawMessage {
	var out []json.RawMessage
	for _, a := range args {
		out = append(out, encoded(a))
	}
	return out
}
func zipBytes(t *testing.T, name string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, err := z.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("native"))
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type fixture struct {
	t      *testing.T
	s      *Service
	server *httptest.Server
	docs   map[string][]byte
	m      metadata
	mu     sync.Mutex
	hits   map[string]int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, docs: map[string][]byte{}, hits: map[string]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits[r.URL.Path]++
		b, ok := f.docs[r.URL.Path]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(f.server.Close)
	c := download.New()
	c.AllowURL = func(u *url.URL) bool { return strings.HasPrefix(u.String(), f.server.URL+"/") }
	c.HTTP = f.server.Client()
	f.s = New(filepath.Join(t.TempDir(), "cache root"), c)
	f.s.manifestURL = f.server.URL + "/manifest"
	f.s.fabricURL = f.server.URL + "/fabric"
	f.s.assetURL = f.server.URL + "/assets"
	f.s.platform = platform{OS: "windows", Arch: "amd64", Version: "10.0.22631"}
	f.s.javaResolver = func(path string, major int) (string, error) {
		if major != 21 {
			return "", fmt.Errorf("unexpected Java major %d", major)
		}
		return "C:/existing Java/bin/java.exe", nil
	}
	f.m.ID = "test-1"
	f.m.Type = "release"
	f.m.MainClass = "net.minecraft.client.main.Main"
	f.m.JavaVersion.MajorVersion = 21
	f.m.Downloads = map[string]artifact{"client": f.art("/client.jar", []byte("client")), "server": f.art("/server.jar", []byte("server"))}
	a := f.art("/lib.jar", []byte("library"))
	a.Path = "example/library/1/library-1.jar"
	var l library
	l.Name = "example:library:1"
	l.Downloads.Artifact = &a
	f.m.Libraries = []library{l}
	native := f.art("/native.jar", zipBytes(t, "native.dll"))
	native.Path = "example/native/1/native-1.jar"
	var n library
	n.Name = "example:native:1"
	n.Natives = map[string]string{"windows": "natives-windows-${arch}"}
	n.Downloads.Classifiers = map[string]artifact{"natives-windows-64": native}
	f.m.Libraries = append(f.m.Libraries, n)
	index := assetIndex{Objects: map[string]struct {
		Hash string `json:"hash"`
		Size int64  `json:"size"`
	}{}}
	for n := 0; n < 12; n++ {
		b := []byte(fmt.Sprintf("asset %d", n))
		h := sha(b)
		f.docs["/assets/"+h[:2]+"/"+h] = b
		index.Objects[fmt.Sprintf("minecraft/sounds/%d.ogg", n)] = struct {
			Hash string `json:"hash"`
			Size int64  `json:"size"`
		}{h, int64(len(b))}
	}
	indexArtifact := f.art("/index.json", encoded(index))
	f.m.AssetIndex.ID = "test-assets"
	f.m.AssetIndex.URL = indexArtifact.URL
	f.m.AssetIndex.SHA1 = indexArtifact.SHA1
	f.m.AssetIndex.Size = indexArtifact.Size
	f.m.Arguments.JVM = rawArgs("-Djava.library.path=${natives_directory}", "-Dminecraft.launcher.brand=${launcher_name}", "-cp", "${classpath}")
	f.m.Arguments.Game = rawArgs("--username", "${auth_player_name}", "--uuid", "${auth_uuid}", "--accessToken", "${auth_access_token}", "--xuid", "${auth_xuid}", "--gameDir", "${game_directory}", "--assetsDir", "${assets_root}", "--assetIndex", "${assets_index_name}")
	f.publish()
	return f
}
func (f *fixture) art(path string, b []byte) artifact {
	f.docs[path] = b
	return artifact{URL: f.server.URL + path, SHA1: sha(b), Size: int64(len(b))}
}
func (f *fixture) publish() {
	b := encoded(f.m)
	f.docs["/version.json"] = b
	f.docs["/manifest"] = encoded(map[string]any{"versions": []any{map[string]any{"id": f.m.ID, "type": "release", "url": f.server.URL + "/version.json", "sha1": sha(b)}}})
}
func identity() Identity {
	return Identity{Name: "Player", UUID: "1234567890abcdef1234567890abcdef", AccessToken: "private-token", XUID: "private-xuid"}
}
func hasArg(args []string, arg string) bool {
	for _, a := range args {
		if a == arg {
			return true
		}
	}
	return false
}
func mustWrite(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func noMarker(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, installationFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed installation wrote success marker", err)
	}
}

func TestVanillaClientInstallAndLaunch(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(t.TempDir(), "game with spaces & chars")
	versions, err := f.s.Versions(context.Background())
	if err != nil || len(versions) != 1 || versions[0].ID != "test-1" {
		t.Fatal(versions, err)
	}
	var progress []download.Progress
	i, err := f.s.InstallClient(context.Background(), dir, Options{Version: "test-1"}, func(p download.Progress) { progress = append(progress, p) })
	if err != nil {
		t.Fatal(err)
	}
	if i.Loader != "vanilla" || i.Kind != "client" || i.JavaMajor != 21 {
		t.Fatal(i)
	}
	if got, err := f.s.ReadInstallation(dir); err != nil || got != i {
		t.Fatal(got, err)
	}
	if _, err = os.Stat(filepath.Join(dir, ".neolauncher", "natives", "native.dll")); err != nil {
		t.Fatal(err)
	}
	spec, err := f.s.ClientLaunch(dir, "", 2048, identity())
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"-Xmx2048M", "Player", "private-token", dir, "net.minecraft.client.main.Main"} {
		if !hasArg(spec.Args, arg) {
			t.Errorf("missing argument %q", arg)
		}
	}
	if strings.Contains(strings.Join(spec.Args, " "), "${") {
		t.Fatal("unresolved placeholder")
	}
	if !reflect.DeepEqual(spec.Secrets, []string{"private-token", "private-xuid"}) {
		t.Fatal("missing redactions")
	}
	if got := string(encoded(spec)); got != "{}" {
		t.Fatal("launch spec serialized", got)
	}
	if got := string(encoded(identity())); got != "{}" {
		t.Fatal("identity serialized", got)
	}
	for _, name := range []string{installationFile, runtimeFile} {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		if bytes.Contains(b, []byte("private-")) {
			t.Fatal("credential persisted")
		}
	}
	completed := 0
	for _, p := range progress {
		if p.Done > p.Total {
			t.Fatal("fabricated progress", p)
		}
		if p.Stage == "Assets" && p.Done == 12 && p.Total == 12 {
			completed++
		}
	}
	if completed != 1 || progress[len(progress)-1].Stage != "Installed" {
		t.Fatal(progress)
	}
	// Reinstall refuses to touch a populated profile.
	if _, err = f.s.InstallClient(context.Background(), dir, Options{Version: "test-1"}, nil); err == nil {
		t.Fatal("reinstalled populated profile")
	}
	second := t.TempDir()
	if _, err = f.s.InstallClient(context.Background(), second, Options{Version: "test-1"}, nil); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hits["/client.jar"] != 1 {
		t.Fatal("verified cache was not reused")
	}
}
func TestHashRejection(t *testing.T) {
	for _, path := range []string{"/version.json", "/client.jar", "/lib.jar", "/index.json", "asset"} {
		t.Run(path, func(t *testing.T) {
			f := newFixture(t)
			if path == "asset" {
				for p := range f.docs {
					if strings.HasPrefix(p, "/assets/") {
						f.docs[p] = []byte("corrupt")
						break
					}
				}
			} else {
				f.docs[path] = []byte("corrupt")
			}
			dir := t.TempDir()
			_, err := f.s.InstallClient(context.Background(), dir, Options{Version: "test-1"}, nil)
			if err == nil {
				t.Fatal("accepted corrupt download")
			}
			noMarker(t, dir)
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatal("failed install leaked profile files", entries)
			}
		})
	}
}
func TestTraversalAndRulesFailClosed(t *testing.T) {
	for _, kind := range []string{"library", "native", "rule", "placeholder"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			switch kind {
			case "library":
				f.m.Libraries[0].Downloads.Artifact.Path = "../outside.jar"
			case "native":
				a := f.art("/native.jar", zipBytes(t, "../outside.dll"))
				a.Path = "example/native/1/native-1.jar"
				f.m.Libraries[1].Downloads.Classifiers["natives-windows-64"] = a
			case "rule":
				f.m.Libraries[0].Rules = []rule{{Action: "allow", Features: map[string]bool{"unknown_feature": true}}}
			case "placeholder":
				f.m.Arguments.Game = rawArgs("${unknown_argument}")
			}
			f.publish()
			dir := t.TempDir()
			_, err := f.s.InstallClient(context.Background(), dir, Options{Version: "test-1"}, nil)
			if kind == "placeholder" {
				if err == nil {
					_, err = f.s.ClientLaunch(dir, "", 1024, identity())
				}
			} else {
				noMarker(t, dir)
			}
			if err == nil {
				t.Fatal("unsafe installation/launch accepted")
			}
		})
	}
}
func TestNativeArchiveSafety(t *testing.T) {
	for _, name := range []string{"../outside.dll", "C:/outside.dll", "dir\\outside.dll", "CON.dll", "/absolute.dll", "META-INF/../../escape"} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "native.zip")
			mustWrite(t, p, zipBytes(t, name))
			if err := extractNatives(p, t.TempDir(), []string{"META-INF/"}); err == nil {
				t.Fatal("accepted unsafe native entry")
			}
		})
	}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	h := &zip.FileHeader{Name: "link"}
	h.SetMode(os.ModeSymlink | 0777)
	w, _ := z.CreateHeader(h)
	w.Write([]byte("outside"))
	z.Close()
	p := filepath.Join(t.TempDir(), "link.zip")
	mustWrite(t, p, b.Bytes())
	if err := extractNatives(p, t.TempDir(), nil); err == nil {
		t.Fatal("accepted symlink")
	}
	b.Reset()
	z = zip.NewWriter(&b)
	h = &zip.FileHeader{Name: "large.dll", UncompressedSize64: maxNativeFileBytes + 1, CompressedSize64: 1}
	w, _ = z.CreateRaw(h)
	w.Write([]byte("x"))
	z.Close()
	mustWrite(t, p, b.Bytes())
	if err := extractNatives(p, t.TempDir(), nil); err == nil {
		t.Fatal("accepted oversized archive")
	}
}
func TestRulesAndArguments(t *testing.T) {
	s := New(t.TempDir(), nil)
	s.platform = platform{OS: "windows", Arch: "amd64", Version: "10.0.22631"}
	cases := []struct {
		rules []rule
		want  bool
		bad   bool
	}{
		{nil, true, false}, {[]rule{{Action: "allow", OS: map[string]string{"name": "linux"}}}, false, false},
		{[]rule{{Action: "allow"}, {Action: "disallow", OS: map[string]string{"name": "windows"}}}, false, false},
		{[]rule{{Action: "allow", OS: map[string]string{"name": "windows", "arch": "x86_64", "version": "^10\\."}}}, true, false},
		{[]rule{{Action: "allow", OS: map[string]string{"arch": "x86"}}}, false, false},
		{[]rule{{Action: "allow", Features: map[string]bool{"is_demo_user": true}}}, false, false},
		{[]rule{{Action: "allow", Features: map[string]bool{"is_demo_user": false}}}, true, false},
		{[]rule{{Action: "mystery"}}, false, true}, {[]rule{{Action: "allow", OS: map[string]string{"future": "x"}}}, false, true},
		{[]rule{{Action: "allow", OS: map[string]string{"arch": "["}}}, false, true},
	}
	for _, tc := range cases {
		got, err := s.allowed(tc.rules)
		if (err != nil) != tc.bad || got != tc.want {
			t.Errorf("rules=%v got=%v err=%v", tc.rules, got, err)
		}
	}
	var r rule
	if json.Unmarshal([]byte(`{"action":"allow","unsupported":true}`), &r) == nil {
		t.Fatal("unknown rule fields ignored")
	}
	raw := []json.RawMessage{json.RawMessage(`{"rules":[{"action":"allow","os":{"name":"windows"}}],"value":["-a","two words"]}`)}
	args, err := s.arguments(raw)
	if err != nil || !reflect.DeepEqual(args, []string{"-a", "two words"}) {
		t.Fatal(args, err)
	}
	if _, err = substitute([]string{"${missing}"}, nil); err == nil {
		t.Fatal("unknown placeholder accepted")
	}
	if _, err = substitute([]string{"${missing"}, nil); err == nil {
		t.Fatal("unfinished placeholder accepted")
	}
}
func (f *fixture) fabric() {
	f.docs["/fabric/versions/loader/test-1"] = []byte(`[{"loader":{"version":"0.16.0","stable":true}}]`)
	var m metadata
	m.ID = "fabric-loader-0.16.0-test-1"
	m.InheritsFrom = "test-1"
	m.MainClass = "net.fabricmc.loader.impl.launch.knot.KnotClient"
	m.Libraries = []library{{Name: "net.fabricmc:fabric-loader:0.16.0", URL: f.server.URL + "/maven/"}}
	p := "/maven/net/fabricmc/fabric-loader/0.16.0/fabric-loader-0.16.0.jar"
	f.docs[p] = []byte("fabric")
	f.docs[p+".sha1"] = []byte(sha(f.docs[p]))
	f.docs["/fabric/versions/loader/test-1/0.16.0/profile/json"] = encoded(m)
	m.MainClass = "net.fabricmc.loader.impl.launch.knot.KnotServer"
	f.docs["/fabric/versions/loader/test-1/0.16.0/server/json"] = encoded(m)
}
func TestFabricClientAndServer(t *testing.T) {
	f := newFixture(t)
	f.fabric()
	dir := t.TempDir()
	i, err := f.s.InstallClient(context.Background(), dir, Options{Version: "test-1", Loader: "fabric"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if i.LoaderVersion != "0.16.0" {
		t.Fatal(i)
	}
	spec, err := f.s.ClientLaunch(dir, "", 2048, identity())
	if err != nil || !hasArg(spec.Args, "net.fabricmc.loader.impl.launch.knot.KnotClient") {
		t.Fatal(spec, err)
	}
	server := t.TempDir()
	_, err = f.s.InstallServer(context.Background(), server, Options{Version: "test-1", Loader: "fabric", LoaderVersion: "0.16.0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic temp fixture only: no real server or EULA is started/accepted.
	mustWrite(t, filepath.Join(server, "eula.txt"), []byte("# unit-test fixture\neula=true\n"))
	spec, err = f.s.ServerLaunch(server, "", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !hasArg(spec.Args, "net.fabricmc.loader.impl.launch.knot.KnotServer") || !hasArg(spec.Args, "nogui") || hasArg(spec.Args, "-jar") {
		t.Fatal(spec)
	}
}
func TestFabricMissingAndBadChecksum(t *testing.T) {
	for _, bad := range []bool{false, true} {
		t.Run(fmt.Sprint(bad), func(t *testing.T) {
			f := newFixture(t)
			f.fabric()
			p := "/maven/net/fabricmc/fabric-loader/0.16.0/fabric-loader-0.16.0.jar.sha1"
			if bad {
				f.docs[p] = []byte(strings.Repeat("0", 40))
			} else {
				delete(f.docs, p)
			}
			dir := t.TempDir()
			if _, err := f.s.InstallClient(context.Background(), dir, Options{Version: "test-1", Loader: "fabric"}, nil); err == nil {
				t.Fatal("accepted absent/bad Maven checksum")
			}
			noMarker(t, dir)
		})
	}
}
func TestServerEULAAndPreservation(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	props := []byte("motd=Keep my settings\n")
	eula := []byte("# eula=true\neula=false\n")
	mustWrite(t, filepath.Join(dir, "server.properties"), props)
	mustWrite(t, filepath.Join(dir, "eula.txt"), eula)
	if _, err := f.s.InstallServer(context.Background(), dir, Options{Version: "test-1"}, nil); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{"server.properties": props, "eula.txt": eula} {
		got, _ := os.ReadFile(filepath.Join(dir, name))
		if !bytes.Equal(got, want) {
			t.Fatal("setup file changed", name)
		}
	}
	for _, text := range []string{"", "#eula=true\n", "!eula=true\n", "eula=false\n", "eula=true # no\n", "eula=true\neula=false\n", "eula=true\neula=true\n", "eula\\=true\n", "other=x\neula=true\n"} {
		mustWrite(t, filepath.Join(dir, "eula.txt"), []byte(text))
		if _, err := f.s.ServerLaunch(dir, "", 1024); err == nil {
			t.Fatalf("accepted ambiguous/unaccepted EULA: %q", text)
		}
	}
	mustWrite(t, filepath.Join(dir, "eula.txt"), []byte("# synthetic fixture\neula = true\n"))
	spec, err := f.s.ServerLaunch(dir, "", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !hasArg(spec.Args, "-jar") || spec.Args[len(spec.Args)-1] != "nogui" {
		t.Fatal(spec)
	}
	f.s.javaResolver = func(string, int) (string, error) { return "", errors.New("Java missing") }
	if _, err = f.s.ServerLaunch(dir, "", 1024); err == nil {
		t.Fatal("missing Java ignored")
	}
	world := t.TempDir()
	os.Mkdir(filepath.Join(world, "world"), 0700)
	if _, err = f.s.InstallServer(context.Background(), world, Options{Version: "test-1"}, nil); err == nil {
		t.Fatal("accepted existing world")
	}
	if _, err = os.Stat(filepath.Join(world, "world")); err != nil {
		t.Fatal("world removed")
	}
}
func TestInstalledMetadataFailsClosed(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	if _, err := f.s.InstallClient(context.Background(), dir, Options{Version: "test-1"}, nil); err != nil {
		t.Fatal(err)
	}
	var rt installedRuntime
	p := filepath.Join(dir, runtimeFile)
	if err := readJSON(p, &rt); err != nil {
		t.Fatal(err)
	}
	rt.Classpath = []string{"cache/libraries/../../../outside.jar"}
	mustWrite(t, p, encoded(rt))
	if _, err := f.s.ClientLaunch(dir, "", 1024, identity()); err == nil {
		t.Fatal("unsafe installed classpath accepted")
	}
	mustWrite(t, filepath.Join(dir, installationFile), []byte(`{"version":"../escape","loader":"vanilla","java_major":21,"kind":"client"}`))
	if _, err := f.s.ReadInstallation(dir); err == nil {
		t.Fatal("unsafe installation record accepted")
	}
}
func TestJavaAndUnsupportedInputs(t *testing.T) {
	for output, want := range map[string]int{`openjdk version "21.0.3" 2024-04-16`: 21, `java version "1.8.0_412"`: 8, `openjdk version "25-ea"`: 25} {
		got, err := parseJavaMajor(output)
		if err != nil || got != want {
			t.Fatal(got, err)
		}
	}
	if _, err := parseJavaMajor("not Java"); err == nil {
		t.Fatal("unrecognized version accepted")
	}
	if _, err := resolveJava(filepath.Join(t.TempDir(), "missing-java.exe"), 21); err == nil {
		t.Fatal("missing Java accepted")
	}
	t.Setenv("JAVA_HOME", t.TempDir())
	if _, err := resolveJava("", 21); err == nil {
		t.Fatal("invalid JAVA_HOME accepted")
	}
	var out boundedOutput
	out.Write(bytes.Repeat([]byte("x"), 100000))
	if len(out.data) != 65536 {
		t.Fatal("unbounded Java output")
	}
	f := newFixture(t)
	for _, o := range []Options{{Version: "test-1", Loader: "forge"}, {Version: "../escape"}, {Version: "test-1", Loader: "vanilla", LoaderVersion: "1"}, {Version: "unknown"}} {
		if _, err := f.s.InstallClient(context.Background(), t.TempDir(), o, nil); err == nil {
			t.Fatal("unsupported options accepted", o)
		}
	}
	for _, n := range []int{-1, 0, 128, 1048577} {
		if _, err := memoryArgs(n); err == nil {
			t.Fatal("invalid memory accepted", n)
		}
	}
}
func TestAssetsConcurrencyAndCancellation(t *testing.T) {
	f := newFixture(t)
	var active, maxSeen atomic.Int32
	old := f.server.Config.Handler
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			n := active.Add(1)
			defer active.Add(-1)
			for {
				m := maxSeen.Load()
				if n <= m || maxSeen.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(15 * time.Millisecond)
		}
		old.ServeHTTP(w, r)
	})
	if _, err := f.s.InstallClient(context.Background(), t.TempDir(), Options{Version: "test-1"}, nil); err != nil {
		t.Fatal(err)
	}
	if n := maxSeen.Load(); n < 2 || n > 6 {
		t.Fatal("asset workers not bounded", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	if _, err := f.s.InstallClient(ctx, dir, Options{Version: "test-1"}, nil); err == nil {
		t.Fatal("cancellation ignored")
	}
	noMarker(t, dir)
}
