//go:build windows

package accounts

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestDPAPIRoundtripPrivateFilesAndLock(t *testing.T) {
	root := t.TempDir()
	s, err := NewService(root, DefaultClientID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if other, err := NewService(root, DefaultClientID); err == nil {
		other.Close()
		t.Fatal("second writer acquired vault")
	}
	if err = s.SetSecret("curseforge-api-key", "fake-private-api-key"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	next := s.cloneLocked()
	next.Accounts[testUUID] = credential{Account: Account{ID: testUUID, UUID: testUUID, Name: "Player_1"}, ClientID: DefaultClientID, RefreshToken: fakeRefresh, AccessToken: fakeMC, XUID: "98765", ExpiresAt: time.Now().Add(time.Hour)}
	err = s.commitLocked(next)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	store := s.store.(*protectedStore)
	file, err := store.filename(vaultName)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{fakeRefresh, fakeMC, "fake-private-api-key", "refresh_token"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("plaintext in credential file")
		}
	}
	if len(raw) == 0 {
		t.Fatal("empty encrypted vault")
	}
	sd, err := windows.GetNamedSecurityInfo(file, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	acl := sd.String()
	if !strings.Contains(acl, "D:P") || !strings.Contains(acl, user.User.Sid.String()) || strings.Contains(acl, ";;;WD)") || strings.Contains(acl, ";;;BU)") {
		t.Fatal("credential DACL is not private")
	}
	entries, err := os.ReadDir(filepath.Join(root, "accounts"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected vault files (temporary file leaked): %d", len(entries))
	}
	s.Close()
	reopened, err := NewService(root, DefaultClientID)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	value, err := reopened.GetSecret("curseforge-api-key")
	if err != nil || value != "fake-private-api-key" {
		t.Fatal("DPAPI secret roundtrip")
	}
	session, err := reopened.Session(context.Background(), testUUID)
	if err != nil || session.AccessToken != fakeMC {
		t.Fatal("DPAPI credential roundtrip")
	}
	if err = reopened.SetSecret("curseforge-api-key", "replacement-key"); err != nil {
		t.Fatal(err)
	}
	if err = reopened.DeleteSecret("curseforge-api-key"); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.GetSecret("curseforge-api-key"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("DPAPI secret delete")
	}
	if err = reopened.Remove(testUUID); err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	final, err := NewService(root, DefaultClientID)
	if err != nil {
		t.Fatal(err)
	}
	defer final.Close()
	list, err := final.List()
	if err != nil || len(list) != 0 {
		t.Fatal("DPAPI deletion not durable")
	}
}

func TestProtectedStoreTraversalTamperingAndEntropy(t *testing.T) {
	rawStore, err := newProtectedStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := rawStore.(*protectedStore)
	defer store.Close()
	for _, name := range []string{"../escape", `..\escape`, "/absolute", `C:\absolute`, "a:stream", "a/b", "a\\b", "..", ""} {
		if _, err := store.filename(name); err == nil {
			t.Errorf("unsafe filename accepted: %q", name)
		}
		if err := store.Set(name, []byte("test")); err == nil {
			t.Errorf("unsafe store write accepted: %q", name)
		}
		if _, err := store.Get(name); err == nil {
			t.Errorf("unsafe store read accepted: %q", name)
		}
	}
	if _, err := store.Get("missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing protected file")
	}
	if err := store.Set("first", []byte("fake-secret")); err != nil {
		t.Fatal(err)
	}
	path, _ := store.filename("first")
	other, _ := store.filename("second")
	if filepath.Dir(path) != store.dir || strings.Contains(filepath.Base(path), "first") {
		t.Fatal("filename not constrained hash")
	}
	ciphertext, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(other, ciphertext, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get("second"); err == nil {
		t.Fatal("DPAPI entropy did not bind storage name")
	}
	ciphertext[len(ciphertext)/2] ^= 0xff
	if err = os.WriteFile(path, ciphertext, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get("first"); err == nil {
		t.Fatal("tampered DPAPI ciphertext accepted")
	}
}

func TestProtectedStoreRejectsSymlink(t *testing.T) {
	root, target := t.TempDir(), t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "accounts")); err != nil {
		t.Skip("symlink creation requires Windows developer mode or privilege")
	}
	if store, err := newProtectedStore(root); err == nil {
		closeStore(store)
		t.Fatal("symlink store accepted")
	}
}
