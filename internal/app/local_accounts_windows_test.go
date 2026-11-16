//go:build windows

package app

import (
	"errors"
	"github.com/Pekly/NeoLauncher/internal/accounts"
	"github.com/Pekly/NeoLauncher/internal/launcher"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalAccountBindingsAndNoMicrosoftFallback(t *testing.T) {
	a, _ := fixtureApp(t, []byte("fixture"))
	state, err := a.CreateLocalAccount("LocalPlayer")
	if err != nil {
		t.Fatal(err)
	}
	local := state.LocalAccounts[0]
	if state.SelectedAccountKind != "local" || len(state.Accounts) != 0 {
		t.Fatal("local identity mixed with Microsoft")
	}
	if _, err = a.SelectLocalAccount(local.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.SelectAccount(local.ID); err == nil {
		t.Fatal("selected local as Microsoft")
	}
	state, err = a.CreateInstance(launcher.CreateInstanceRequest{Name: "Local game", Version: "1.21.1"})
	if err != nil {
		t.Fatal(err)
	}
	id := state.Instances[0].ID
	if _, err = a.StartClient(id); err == nil || !strings.Contains(err.Error(), "install") {
		t.Fatal("uninstalled game started", err)
	}
	profile, _ := a.store.GetProfile("client", id)
	a.accounts.Close()
	state.SelectedAccountKind = "microsoft"
	state.SelectedAccountID = "1234567890abcdef1234567890abcdef"
	if _, err = a.clientLaunchSpec(profile, state.State); !errors.Is(err, accounts.ErrClosed) {
		t.Fatal("Microsoft failure fell back to local", err)
	}
	state.SelectedAccountKind = "local"
	state.SelectedLocalAccountID = "missing"
	if _, err = a.clientLaunchSpec(profile, state.State); err == nil || !strings.Contains(err.Error(), "select a local") {
		t.Fatal("missing local selected", err)
	}
	state, err = a.RemoveLocalAccount(local.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.LocalAccounts) != 0 || state.SelectedAccountKind != "local" || state.SelectedLocalAccountID != "" {
		t.Fatal("removal changed identity mode")
	}
	if _, err = a.CreateLocalAccount("../bad"); err == nil {
		t.Fatal("invalid name accepted")
	}
}

func TestLocalJavaFixtureLaunchWithoutMicrosoft(t *testing.T) {
	java, jarPath := os.Getenv("NEOLAUNCHER_TEST_JAVA"), os.Getenv("NEOLAUNCHER_TEST_CLIENT_JAR")
	if java == "" || jarPath == "" {
		t.Skip("set NEOLAUNCHER_TEST_JAVA and NEOLAUNCHER_TEST_CLIENT_JAR for synthetic Java client test")
	}
	jar, err := os.ReadFile(jarPath)
	if err != nil {
		t.Fatal(err)
	}
	a, calls := fixtureApp(t, jar)
	settings := a.GetState().Settings
	settings.JavaPath = java
	if _, err = a.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if _, err = a.CreateLocalAccount("OfflineTest"); err != nil {
		t.Fatal(err)
	}
	state, err := a.CreateInstance(launcher.CreateInstanceRequest{Name: "Offline fixture", Version: "1.21.1"})
	if err != nil {
		t.Fatal(err)
	}
	id := state.Instances[0].ID
	op, err := a.InstallProfile("client", id)
	if err != nil {
		t.Fatal(err)
	}
	if done := waitOperation(t, a, op.ID); done.State != "complete" {
		t.Fatal(done.Error)
	}
	before := calls.Load()
	a.accounts.Close()
	if _, err = a.StartClient(id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for a.runner.Active("client", id) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if a.runner.ActiveCount() != 0 {
		t.Fatal("fixture did not exit")
	}
	if calls.Load() != before {
		t.Fatal("local launch made download requests")
	}
	profile, _ := a.store.GetProfile("client", id)
	b, err := os.ReadFile(filepath.Join(profile.Directory, "local-client-args.txt"))
	if err != nil || string(b) != "--username\nOfflineTest" {
		t.Fatal("wrong local player reached Java", string(b), err, a.runner.Logs("client", id))
	}
	t.Log("Synthetic Java client launched with the selected local name, account service closed, no download requests; no real gameplay exercised")
}
