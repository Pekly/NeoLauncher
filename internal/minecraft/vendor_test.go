package minecraft

import (
	"context"
	"os"
	"testing"
	"time"
)

// Explicit opt-in contract check: metadata and checksum sidecars only. It does
// not download game/library jars, install a game, launch Java or accept an EULA.
func TestLiveVendorMetadata(t *testing.T) {
	if os.Getenv("NEOLAUNCHER_TEST_VENDOR_METADATA") != "1" {
		t.Skip("opt-in public vendor metadata check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := New(t.TempDir(), nil)
	versions, err := s.Versions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	version := ""
	for _, v := range versions {
		if v.Type == "release" {
			version = v.ID
			break
		}
	}
	if version == "" {
		t.Fatal("no official release")
	}
	m, err := s.versionMetadata(ctx, version, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.arguments(m.Arguments.JVM); err != nil {
		t.Fatal(err)
	}
	if _, err = s.gameArguments(m); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, l := range m.Libraries {
		allowed, e := s.allowed(l.Rules)
		if e != nil {
			t.Fatal(e)
		}
		if allowed {
			if _, e = s.libraryArtifact(ctx, l); e != nil {
				t.Fatal(e)
			}
			count++
		}
	}
	for _, kind := range []string{"client", "server"} {
		f, loader, e := s.fabricProfile(ctx, Options{Version: version, Loader: "fabric"}, kind)
		if e != nil {
			t.Fatal(e)
		}
		for _, l := range f.Libraries {
			if _, e = s.libraryArtifact(ctx, l); e != nil {
				t.Fatal(e)
			}
		}
		t.Logf("official %s / Fabric %s %s profile: %s, %d libraries", version, loader, kind, f.MainClass, len(f.Libraries))
	}
	t.Logf("official release %s, Java %d, %d platform-selected libraries", version, m.JavaVersion.MajorVersion, count)
}
