package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSettingsAndProfilesPersist(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	state := store.GetState()
	if state.Settings.ClientMemoryMB != 4096 || state.NextServerPort != 25565 || state.Servers == nil {
		t.Fatal(state)
	}
	if _, err = store.SaveSettings(Settings{JavaPath: `C:\Java with spaces\bin\java.exe`, ClientMemoryMB: 6144}); err != nil {
		t.Fatal(err)
	}
	state, err = store.CreateServer(CreateServerRequest{Name: "Survival", Version: "1.21.1", Port: 25565, MemoryMB: 2048})
	if err != nil {
		t.Fatal(err)
	}
	profile := state.Servers[0]
	eula, err := os.ReadFile(filepath.Join(profile.Directory, "eula.txt"))
	if err != nil || !strings.Contains(string(eula), "eula=false") {
		t.Fatal("EULA accepted or absent", err)
	}
	properties, err := os.ReadFile(filepath.Join(profile.Directory, "server.properties"))
	if err != nil || !strings.Contains(string(properties), "online-mode=true") {
		t.Fatal("unsafe server defaults", err)
	}
	files, err := os.ReadDir(profile.Directory)
	if err != nil || len(files) != 2 {
		t.Fatal("unexpected downloaded/launched files", err)
	}
	reloaded, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	saved := reloaded.GetState()
	if saved.Settings.ClientMemoryMB != 6144 || len(saved.Servers) != 1 || saved.Servers[0].Name != "Survival" || saved.NextServerPort != 25566 {
		t.Fatal(saved)
	}
	state.Servers[0].Name = "changed outside store"
	if store.GetState().Servers[0].Name != "Survival" {
		t.Fatal("snapshot aliases internal state")
	}
}
func TestInvalidInputsDoNotCreateFiles(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []CreateServerRequest{{Name: "", Port: 25565, MemoryMB: 2048}, {Name: "bad\nname", Port: 25565, MemoryMB: 2048}, {Name: "Test", Version: "../../outside", Port: 25565, MemoryMB: 2048}, {Name: "Test", Port: 80, MemoryMB: 2048}, {Name: "Test", Port: 25565, MemoryMB: 0}} {
		if _, err := store.CreateServer(request); err == nil {
			t.Fatal("invalid input accepted", request)
		}
	}
	if _, err := store.SaveSettings(Settings{JavaPath: "bad\x00path", ClientMemoryMB: 4096}); err == nil {
		t.Fatal("invalid path accepted")
	}
	if _, err := store.SaveSettings(Settings{ClientMemoryMB: 40000}); err == nil {
		t.Fatal("invalid memory accepted")
	}
	files, err := os.ReadDir(filepath.Join(store.root, "servers"))
	if err != nil || len(files) != 0 {
		t.Fatal("invalid input wrote files", err)
	}
	if _, err := store.ServerDirectory("../outside"); err == nil {
		t.Fatal("unknown profile path accepted")
	}
}
func TestDuplicatePortIsAtomic(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.CreateServer(CreateServerRequest{Name: "Server", Port: 25565, MemoryMB: 2048})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 || len(store.GetState().Servers) != 1 {
		t.Fatal("duplicate profiles", success)
	}
}
func TestCorruptStateIsNotSilentlyReset(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	original := []byte("not JSON")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err == nil {
		t.Fatal("corrupt settings accepted")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Fatal("corrupt settings destroyed")
	}
}
func TestStoredDirectoryCannotRedirectFolderAction(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.CreateServer(CreateServerRequest{Name: "../Display name only", Port: 25565, MemoryMB: 2048})
	if err != nil {
		t.Fatal(err)
	}
	state.Servers[0].Directory = filepath.Join(t.TempDir(), "outside")
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(store.root, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Open(store.root)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := reloaded.ServerDirectory(state.Servers[0].ID)
	if err != nil || dir != store.directory(state.Servers[0].ID) {
		t.Fatal("redirected directory", dir, err)
	}
}
func TestFailedPersistenceRollsBackNewProfile(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(store.root, "state.json")
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateServer(CreateServerRequest{Name: "Server", Port: 25565, MemoryMB: 2048}); err == nil {
		t.Fatal("persistence unexpectedly succeeded")
	}
	entries, _ := os.ReadDir(filepath.Join(store.root, "servers"))
	if len(entries) != 0 || len(store.GetState().Servers) != 0 {
		t.Fatal("orphaned profile after failed save")
	}
}
