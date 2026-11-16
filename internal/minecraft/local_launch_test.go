package minecraft

import (
	"context"
	"strings"
	"testing"
)

func TestLocalLaunchUsesInstalledFilesWithoutAuthentication(t *testing.T) {
	f := newFixture(t)
	f.m.Arguments.Game = append(f.m.Arguments.Game, rawArgs("--userType", "${user_type}", "--session", "${auth_session}")...)
	f.publish()
	dir := t.TempDir()
	if _, err := f.s.InstallClient(context.Background(), dir, Options{Version: "test-1"}, nil); err != nil {
		t.Fatal(err)
	}
	f.server.Close() // No vendor service remains available for the launch.
	spec, err := f.s.LocalClientLaunch(dir, "", 2048, "Steve")
	if err != nil {
		t.Fatal(err)
	}
	for flag, want := range map[string]string{"--username": "Steve", "--uuid": "5627dd98e6be3c21b8a8e92344183641", "--accessToken": "0", "--xuid": "", "--userType": "legacy", "--session": "0"} {
		found := false
		for i, arg := range spec.Args {
			if arg == flag && i+1 < len(spec.Args) {
				found = true
				if spec.Args[i+1] != want {
					t.Fatalf("%s: %q", flag, spec.Args[i+1])
				}
			}
		}
		if !found {
			t.Fatal("missing argument", flag)
		}
	}
	if len(spec.Secrets) != 0 {
		t.Fatal("local launch carried secrets")
	}
	if _, err = f.s.ClientLaunch(dir, "", 2048, Identity{Name: "Steve", UUID: "5627dd98e6be3c21b8a8e92344183641"}); err == nil {
		t.Fatal("Microsoft launch silently allowed a missing token")
	}
	if _, err = f.s.LocalClientLaunch(dir, "", 2048, "bad name"); err == nil {
		t.Fatal("invalid local name accepted")
	}
	if _, err = f.s.LocalClientLaunch(t.TempDir(), "", 2048, "Steve"); err == nil {
		t.Fatal("missing install accepted")
	}
	if strings.Contains(strings.Join(spec.Args, " "), "private-token") {
		t.Fatal("fixture credentials leaked into local launch")
	}
}
