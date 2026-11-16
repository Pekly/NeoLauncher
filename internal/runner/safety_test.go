package runner

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConsoleRedactsAfterANSINormalization(t *testing.T) {
	m := New()
	e := &entry{secrets: []string{"secret-token-value"}}
	w := &lineWriter{append: func(line string) { m.append(e, line) }}
	for _, b := range []byte("secret-\x1b[31mtoken\x1b[0m-value\n") {
		_, _ = w.Write([]byte{b})
	}
	if len(e.logs) != 1 || e.logs[0] != "[redacted]" {
		t.Fatal("ANSI-split token leaked", e.logs)
	}
	for _, suffix := range []string{"\n", ""} {
		_, _ = w.Write([]byte(strings.Repeat("x", 8188) + "secret-token-value" + suffix))
		w.flush()
		if e.logs[len(e.logs)-1] != "[oversized line omitted]" {
			t.Fatal("truncated token prefix leaked")
		}
	}
}

func TestConcurrentStartAndShutdown(t *testing.T) {
	t.Setenv("NEOLAUNCHER_RUNNER_HELPER", "1")
	for n := 0; n < 12; n++ {
		m := New()
		dir := t.TempDir()
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_ = m.Start("server", "race", Spec{Program: os.Args[0], Args: []string{"-test.run=^TestRunnerHelper$"}, Directory: dir}, 512)
		}()
		go func() {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = m.Shutdown(ctx)
		}()
		close(start)
		wg.Wait()
		if m.ActiveCount() != 0 {
			t.Fatal("process survived shutdown")
		}
		if err := m.Start("server", "late", Spec{Program: os.Args[0], Directory: dir}, 512); err == nil {
			t.Fatal("start accepted after shutdown")
		}
	}
}
