package runner

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunnerHelper(t *testing.T) {
	if os.Getenv("NEOLAUNCHER_RUNNER_HELPER") != "1" {
		return
	}
	fmt.Println("secret-token-value")
	fmt.Println("Done (0.1s)! For help, type help")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if scanner.Text() == "stop" && os.Getenv("NEOLAUNCHER_RUNNER_HANG") != "1" {
			_ = os.WriteFile("saved.txt", []byte("saved"), 0600)
			os.Exit(0)
		}
		fmt.Println("command received")
	}
	time.Sleep(time.Hour)
}
func helper(t *testing.T) (*Manager, string) {
	t.Helper()
	t.Setenv("NEOLAUNCHER_RUNNER_HELPER", "1")
	directory := t.TempDir()
	m := New()
	if err := m.Start("server", "test", Spec{Program: os.Args[0], Args: []string{"-test.run=^TestRunnerHelper$"}, Directory: directory, Secrets: []string{"secret-token-value"}}, 512); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(m.Statuses()) == 1 && m.Statuses()[0].Status == "running" {
			return m, directory
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("process never became ready", m.Logs("server", "test"))
	return nil, ""
}
func TestGracefulServerStopAndRedactedLogs(t *testing.T) {
	m, directory := helper(t)
	if strings.Contains(strings.Join(m.Logs("server", "test"), "\n"), "secret-token-value") {
		t.Fatal("token leaked")
	}
	if err := m.Send("test", "list"); err != nil {
		t.Fatal(err)
	}
	if err := m.Send("test", "list\nstop"); err == nil {
		t.Fatal("multiline command accepted")
	}
	if err := m.Start("server", "test", Spec{Program: os.Args[0], Directory: directory}, 512); err == nil {
		t.Fatal("duplicate process started")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Stop(ctx, "server", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "saved.txt")); err != nil {
		t.Fatal("graceful stop not sent", err)
	}
	if m.ActiveCount() != 0 || m.Statuses()[0].Status != "stopped" {
		t.Fatal("process still active")
	}
}
func TestForcedStopIsBounded(t *testing.T) {
	t.Setenv("NEOLAUNCHER_RUNNER_HANG", "1")
	m, _ := helper(t)
	m.stopTimeout = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Stop(ctx, "server", "test"); err == nil {
		t.Fatal("forced stop not reported")
	}
	if m.ActiveCount() != 0 {
		t.Fatal("forced process remains")
	}
}
func TestStartFailureDoesNotReserveProfile(t *testing.T) {
	m := New()
	spec := Spec{Program: filepath.Join(t.TempDir(), "missing-executable"), Directory: t.TempDir()}
	if err := m.Start("server", "failed", spec, 512); err == nil {
		t.Fatal("missing executable started")
	}
	if m.ActiveCount() != 0 {
		t.Fatal("failed launch stuck active")
	}
	if err := m.Stop(context.Background(), "server", "failed"); err != nil {
		t.Fatal(err)
	}
}
func TestLongOutputIsBounded(t *testing.T) {
	var output []string
	w := &lineWriter{append: func(line string) { output = append(output, line) }}
	w.Write([]byte(strings.Repeat("x", 1<<20) + "\nnext"))
	w.flush()
	if len(output) != 2 || len(output[0]) > 8250 || output[1] != "next" {
		t.Fatal("unbounded output")
	}
}
