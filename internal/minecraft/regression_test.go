package minecraft

import (
	"context"
	"encoding/json"
	"testing"
)

func TestOfficialIDsWithSpaces(t *testing.T) {
	f := newFixture(t)
	f.m.ID = "1.14 Pre-Release 1"
	f.publish()
	versions, err := f.s.Versions(context.Background())
	if err != nil || len(versions) != 1 || versions[0].ID != f.m.ID {
		t.Fatal(versions, err)
	}
	if _, err = f.s.InstallClient(context.Background(), t.TempDir(), Options{Version: f.m.ID}, nil); err != nil {
		t.Fatal(err)
	}
}
func TestRepublishedAssetIndexDoesNotBreakExistingProfile(t *testing.T) {
	f := newFixture(t)
	first := t.TempDir()
	if _, err := f.s.InstallClient(context.Background(), first, Options{Version: "test-1"}, nil); err != nil {
		t.Fatal(err)
	}
	var index assetIndex
	if err := json.Unmarshal(f.docs["/index.json"], &index); err != nil {
		t.Fatal(err)
	}
	delete(index.Objects, "minecraft/sounds/0.ogg")
	a := f.art("/index.json", encoded(index))
	f.m.AssetIndex.SHA1 = a.SHA1
	f.m.AssetIndex.Size = a.Size
	f.publish()
	if _, err := f.s.InstallClient(context.Background(), t.TempDir(), Options{Version: "test-1"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ClientLaunch(first, "", 1024, identity()); err != nil {
		t.Fatal("old index overwritten", err)
	}
}
func TestNullArgumentsRejected(t *testing.T) {
	s := New(t.TempDir(), nil)
	for _, text := range []string{`null`, `{"rules":[{"action":"allow"}],"value":null}`, `{"rules":[{"action":"allow"}]}`} {
		if _, err := s.arguments([]json.RawMessage{json.RawMessage(text)}); err == nil {
			t.Fatal("invalid argument accepted", text)
		}
	}
}
func TestConcurrentProfilesShareVerifiedCache(t *testing.T) {
	f := newFixture(t)
	dirs := []string{t.TempDir(), t.TempDir()}
	results := make(chan error, 2)
	for _, dir := range dirs {
		go func() {
			_, err := f.s.InstallClient(context.Background(), dir, Options{Version: "test-1"}, nil)
			results <- err
		}()
	}
	for range dirs {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hits["/client.jar"] != 1 {
		t.Fatal("cache download duplicated", f.hits["/client.jar"])
	}
}
