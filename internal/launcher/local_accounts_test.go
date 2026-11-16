package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLocalAccountsPersistenceAndExplicitSelection(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if s.GetState().SelectedAccountKind != "microsoft" || s.GetState().LocalAccounts == nil {
		t.Fatal("bad legacy defaults")
	}
	ms := "1234567890abcdef1234567890abcdef"
	if err = s.SelectAccount(ms); err != nil {
		t.Fatal(err)
	}
	local, err := s.CreateLocalAccount(" Steve ")
	if err != nil {
		t.Fatal(err)
	}
	if local.Name != "Steve" || !strings.HasPrefix(local.ID, "local-") {
		t.Fatal(local)
	}
	if _, err = s.CreateLocalAccount("steve"); err == nil {
		t.Fatal("case-ambiguous duplicate allowed")
	}
	state := s.GetState()
	if state.SelectedAccountKind != "local" || state.SelectedLocalAccountID != local.ID || state.SelectedAccountID != ms {
		t.Fatal("identity selection lost")
	}
	state.LocalAccounts[0].Name = "mutated"
	if s.GetState().LocalAccounts[0].Name != "Steve" {
		t.Fatal("state leaked mutable slice")
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.GetState().LocalAccounts[0] != local || reopened.GetState().SelectedAccountKind != "local" {
		t.Fatal("local identity changed on reopen")
	}
	if err = s.SelectAccount(ms); err != nil {
		t.Fatal(err)
	}
	if s.GetState().SelectedAccountKind != "microsoft" {
		t.Fatal("Microsoft selection did not change mode")
	}
	if err = s.SelectLocalAccount(local.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.SelectAccount(""); err != nil {
		t.Fatal(err)
	}
	if s.GetState().SelectedAccountKind != "local" {
		t.Fatal("Microsoft removal changed local mode")
	}
	sentinel := filepath.Join(root, "instances", "world-sentinel")
	if err = os.WriteFile(sentinel, []byte("world"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.RemoveLocalAccount(local.ID); err != nil {
		t.Fatal(err)
	}
	if s.GetState().SelectedLocalAccountID != "" || s.GetState().SelectedAccountKind != "local" {
		t.Fatal("removal implicitly selected another identity")
	}
	if b, err := os.ReadFile(sentinel); err != nil || string(b) != "world" {
		t.Fatal("removal changed world", err)
	}
	again, err := s.CreateLocalAccount("Steve")
	if err != nil || again != local {
		t.Fatal("recreated local identity changed", err)
	}
	if err = s.SelectAccount(local.ID); err == nil {
		t.Fatal("local id accepted as Microsoft")
	}
	if err = s.SelectLocalAccount(ms); err == nil {
		t.Fatal("Microsoft id accepted as local")
	}
}

func TestLocalAccountsValidationAndRollback(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "two words", "../name", strings.Repeat("a", 17)} {
		if _, err = s.CreateLocalAccount(name); err == nil {
			t.Fatal("invalid name", name)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.CreateLocalAccount("SameName") }()
	}
	wg.Wait()
	if len(s.GetState().LocalAccounts) != 1 {
		t.Fatal("duplicate concurrent accounts")
	}
	path := filepath.Join(s.root, "state.json")
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	before := s.GetState()
	if _, err = s.CreateLocalAccount("FailedSave"); err == nil {
		t.Fatal("save should fail")
	}
	if err = s.RemoveLocalAccount(before.LocalAccounts[0].ID); err == nil {
		t.Fatal("remove save should fail")
	}
	if len(s.GetState().LocalAccounts) != 1 || s.GetState().SelectedLocalAccountID != before.SelectedLocalAccountID {
		t.Fatal("failed write mutated state")
	}
}

func TestLocalAccountsRejectCorruptState(t *testing.T) {
	for _, kind := range []string{"uuid", "id", "name", "selected", "kind", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			s, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.CreateLocalAccount("Steve"); err != nil {
				t.Fatal(err)
			}
			state := s.GetState()
			switch kind {
			case "uuid":
				state.LocalAccounts[0].UUID = strings.Repeat("a", 32)
			case "id":
				state.LocalAccounts[0].ID = "microsoft-id"
			case "name":
				state.LocalAccounts[0].Name = "../bad"
			case "selected":
				state.SelectedLocalAccountID = "missing"
			case "kind":
				state.SelectedAccountKind = "unknown"
			case "duplicate":
				state.LocalAccounts = append(state.LocalAccounts, state.LocalAccounts[0])
			}
			data, _ := json.Marshal(state)
			file := filepath.Join(root, "state.json")
			if err = os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = Open(root); err == nil {
				t.Fatal("corrupt account state accepted")
			}
			after, _ := os.ReadFile(file)
			if string(after) != string(data) {
				t.Fatal("corrupt data overwritten")
			}
		})
	}
}
