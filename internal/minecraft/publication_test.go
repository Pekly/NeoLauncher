package minecraft

import (
	"context"
	"errors"
	"github.com/Pekly/NeoLauncher/internal/download"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFailedPublicationRollsBackAndRetries(t *testing.T) {
	for _, blocker := range []string{"server.jar", runtimeFile, installationFile} {
		t.Run(blocker, func(t *testing.T) {
			f := newFixture(t)
			dir := t.TempDir()
			mustWrite(t, filepath.Join(dir, "eula.txt"), []byte("eula=false\n"))
			created := false
			_, err := f.s.InstallServer(context.Background(), dir, Options{Version: "test-1"}, func(p download.Progress) {
				if !created && p.Stage == "Game jar" {
					created = true
					mustWrite(t, filepath.Join(dir, blocker), []byte("concurrent user file"))
				}
			})
			if err == nil || !created {
				t.Fatal("expected publication failure", err)
			}
			b, err := os.ReadFile(filepath.Join(dir, blocker))
			if err != nil || string(b) != "concurrent user file" {
				t.Fatal("removed caller file", err)
			}
			if err = os.Remove(filepath.Join(dir, blocker)); err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 || entries[0].Name() != "eula.txt" {
				t.Fatal("installation artifacts left behind", entries)
			}
			if _, err = f.s.InstallServer(context.Background(), dir, Options{Version: "test-1"}, nil); err != nil {
				t.Fatal("retry failed", err)
			}
			b, _ = os.ReadFile(filepath.Join(dir, "eula.txt"))
			if string(b) != "eula=false\n" {
				t.Fatal("changed EULA")
			}
		})
	}
}

func TestEULAAcceptancePreservesLinkedTargets(t *testing.T) {
	s := New(t.TempDir(), nil)
	t.Run("regular", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, filepath.Join(dir, "eula.txt"), []byte("eula=false\n"))
		if err := s.AcceptEULA(dir); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(dir, "eula.txt"))
		if !strings.Contains(string(b), "eula=true") {
			t.Fatal("consent not saved")
		}
	})
	for _, kind := range []string{"symlink", "hardlink", "ancestor"} {
		t.Run(kind, func(t *testing.T) {
			dir, outside := t.TempDir(), t.TempDir()
			sentinel := filepath.Join(outside, "eula.txt")
			mustWrite(t, sentinel, []byte("sentinel"))
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(sentinel, filepath.Join(dir, "eula.txt"))
			case "hardlink":
				err = os.Link(sentinel, filepath.Join(dir, "eula.txt"))
			case "ancestor":
				err = os.Symlink(outside, filepath.Join(dir, "linked"))
				dir = filepath.Join(dir, "linked")
			}
			if err != nil {
				t.Skipf("link unavailable: %v", err)
			}
			err = s.AcceptEULA(dir)
			if kind != "hardlink" && err == nil {
				t.Fatal("accepted linked path")
			}
			b, readErr := os.ReadFile(sentinel)
			if readErr != nil || string(b) != "sentinel" {
				t.Fatal("modified linked target", readErr)
			}
			leftovers, _ := filepath.Glob(filepath.Join(dir, ".eula-*"))
			if len(leftovers) > 0 {
				t.Fatal("temporary consent file leaked")
			}
		})
	}
	if err := s.AcceptEULA(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
