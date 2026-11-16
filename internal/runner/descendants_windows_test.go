//go:build windows

package runner

import (
	"bufio"
	"context"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestTreeHelper(t *testing.T) {
	switch os.Getenv("NEOLAUNCHER_TREE_HELPER") {
	case "child":
		time.Sleep(time.Hour)
		os.Exit(0)
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestTreeHelper$")
		child.Env = append(os.Environ(), "NEOLAUNCHER_TREE_HELPER=child")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile("child.pid", []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			os.Exit(3)
		}
		fmt.Println("Done (0.1s)! For help, type help")
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			if scanner.Text() == "stop" {
				os.Exit(0)
			}
		}
		time.Sleep(time.Hour)
	}
}

func TestOwnedDescendantsExitWithServer(t *testing.T) {
	t.Setenv("NEOLAUNCHER_TREE_HELPER", "parent")
	dir := t.TempDir()
	m := New()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	if err := m.Start("server", "tree", Spec{Program: os.Args[0], Args: []string{"-test.run=^TestTreeHelper$"}, Directory: dir}, 512); err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join(dir, "child.pid"))
		if err == nil {
			pid, _ = strconv.Atoi(string(b))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("child did not start", m.Logs("server", "tree"))
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	defer windows.TerminateProcess(handle, 1) // owned test child only, even if assertion fails
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = m.Stop(ctx, "server", "tree"); err != nil {
		t.Fatal(err)
	}
	result, err := windows.WaitForSingleObject(handle, 5000)
	if err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatal("descendant survived server exit", result, err)
	}
}
