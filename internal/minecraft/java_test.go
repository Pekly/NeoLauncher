package minecraft

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a tiny Java-version responder. No game process runs.
func TestMain(m *testing.M) {
	if mode := os.Getenv("NEOLAUNCHER_JAVA_VERSION_HELPER"); mode != "" {
		if len(os.Args) != 2 || os.Args[1] != "-version" {
			os.Exit(3)
		}
		if mode == "timeout" {
			time.Sleep(30 * time.Second)
			os.Exit(4)
		}
		fmt.Fprintf(os.Stderr, "openjdk version \"%s.0.1\"\n", mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestJavaVersionCommand(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEOLAUNCHER_JAVA_VERSION_HELPER", "21")
	got, err := resolveJava(exe, 21)
	if err != nil || got != exe {
		t.Fatal(got, err)
	}
	if _, err = resolveJava(exe, 25); err == nil || !strings.Contains(err.Error(), "requires Java 25") {
		t.Fatal("wrong major accepted", err)
	}
	// Explicit configuration must win over a broken JAVA_HOME.
	t.Setenv("JAVA_HOME", filepath.Join(t.TempDir(), "missing"))
	if _, err = resolveJava(exe, 21); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEOLAUNCHER_JAVA_VERSION_HELPER", "timeout")
	start := time.Now()
	if _, err = resolveJava(exe, 21); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatal("unbounded version check", err)
	}
	if time.Since(start) > 8*time.Second {
		t.Fatal("Java version check exceeded its bound")
	}
}
