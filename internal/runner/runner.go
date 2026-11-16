// Package runner owns local game/server processes. It is not a sandbox for mods.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Spec struct {
	Program   string
	Args      []string
	Directory string
	Secrets   []string
}
type Status struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	ExitCode  int       `json:"exit_code"`
	Error     string    `json:"error,omitempty"`
}
type owner interface {
	Kill() error
	Close() error
}
type entry struct {
	status      Status
	command     *exec.Cmd
	stdin       io.WriteCloser
	inputMu     sync.Mutex
	ownership   owner
	ready, done chan struct{}
	logs        []string
	secrets     []string
}
type Manager struct {
	mu          sync.Mutex
	entries     map[string]*entry
	closing     bool
	stopTimeout time.Duration
}

func New() *Manager              { return &Manager{entries: map[string]*entry{}, stopTimeout: 30 * time.Second} }
func key(kind, id string) string { return kind + ":" + id }
func (m *Manager) Start(kind, id string, spec Spec, memoryMB int) error {
	if (kind != "client" && kind != "server") || id == "" || spec.Program == "" || spec.Directory == "" {
		return errors.New("invalid launch configuration")
	}
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return errors.New("launcher is closing")
	}
	if previous := m.entries[key(kind, id)]; previous != nil {
		select {
		case <-previous.done:
		default:
			m.mu.Unlock()
			return errors.New("this profile is already running")
		}
	}
	active := 0
	for _, e := range m.entries {
		select {
		case <-e.done:
		default:
			active++
		}
	}
	if active >= 8 {
		m.mu.Unlock()
		return errors.New("stop a running game or server first (maximum 8)")
	}
	command := exec.Command(spec.Program, spec.Args...)
	command.Dir = spec.Directory
	command.WaitDelay = 2 * time.Second
	e := &entry{status: Status{ID: id, Kind: kind, Status: "starting", StartedAt: time.Now().UTC(), ExitCode: -1}, command: command, ready: make(chan struct{}), done: make(chan struct{}), secrets: append([]string{}, spec.Secrets...), logs: []string{"Starting " + kind + "…"}}
	m.entries[key(kind, id)] = e
	m.mu.Unlock()
	failed := func(err error) error {
		m.mu.Lock()
		e.status.Status = "stopped"
		e.status.Error = "Could not start Java."
		e.secrets = nil
		e.command.Args = nil
		close(e.ready)
		close(e.done)
		m.mu.Unlock()
		return fmt.Errorf("could not start Java: %w", err)
	}
	stdout, stderr := &lineWriter{append: func(line string) { m.append(e, line) }}, &lineWriter{append: func(line string) { m.append(e, "[stderr] "+line) }}
	command.Stdout = stdout
	command.Stderr = stderr
	var err error
	e.stdin, err = command.StdinPipe()
	if err != nil {
		return failed(err)
	}
	e.ownership, err = startOwned(command, memoryMB)
	if err != nil {
		e.stdin.Close()
		return failed(err)
	}
	m.mu.Lock()
	closing := m.closing
	if closing {
		e.status.Status = "stopping"
	} else if kind == "client" {
		e.status.Status = "running"
	}
	close(e.ready)
	m.mu.Unlock()
	if closing {
		_ = e.ownership.Kill()
	}
	go func() {
		err := command.Wait()
		stdout.flush()
		stderr.flush()
		e.ownership.Close()
		m.mu.Lock()
		e.status.ExitCode = command.ProcessState.ExitCode()
		e.status.Status = "stopped"
		if err != nil && e.status.ExitCode != 0 {
			e.status.Error = fmt.Sprintf("Process exited with code %d. Check the console.", e.status.ExitCode)
		}
		e.logs = append(e.logs, "Process stopped.")
		if len(e.logs) > 600 {
			e.logs = e.logs[len(e.logs)-600:]
		}
		e.secrets = nil
		e.command.Args = nil
		e.command.Env = nil
		close(e.done)
		m.mu.Unlock()
	}()
	return nil
}

var ansi = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

func (m *Manager) append(e *entry, line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	line = ansi.ReplaceAllString(strings.TrimRight(line, "\r"), "")
	for _, secret := range e.secrets {
		if secret != "" {
			line = strings.ReplaceAll(line, secret, "[redacted]")
		}
	}
	if e.status.Kind == "server" && e.status.Status == "starting" && strings.Contains(line, "Done (") && strings.Contains(line, "For help") {
		e.status.Status = "running"
	}
	e.logs = append(e.logs, line)
	if len(e.logs) > 600 {
		e.logs = e.logs[len(e.logs)-600:]
	}
}
func (m *Manager) Statuses() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, e.status)
	}
	return out
}
func (m *Manager) Active(kind, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[key(kind, id)]
	if e == nil {
		return false
	}
	select {
	case <-e.done:
		return false
	default:
		return true
	}
}
func (m *Manager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range m.entries {
		select {
		case <-e.done:
		default:
			n++
		}
	}
	return n
}
func (m *Manager) Logs(kind, id string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.entries[key(kind, id)]; e != nil {
		return append([]string{}, e.logs...)
	}
	return []string{}
}
func (m *Manager) Send(id, command string) error {
	command = strings.TrimSpace(command)
	if command == "" || len(command) > 2048 || strings.ContainsAny(command, "\x00\r\n") {
		return errors.New("enter one console command")
	}
	m.mu.Lock()
	e := m.entries[key("server", id)]
	if e == nil || e.status.Status == "stopping" || e.status.Status == "stopped" {
		m.mu.Unlock()
		return errors.New("server is not accepting commands")
	}
	m.mu.Unlock()
	select {
	case <-e.ready:
	default:
		return errors.New("server is still starting")
	}
	if !e.inputMu.TryLock() {
		return errors.New("console input is busy")
	}
	result := make(chan error, 1)
	go func() { defer e.inputMu.Unlock(); _, err := io.WriteString(e.stdin, command+"\n"); result <- err }()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		_ = e.stdin.Close()
		return errors.New("console input timed out")
	}
}
func (m *Manager) Stop(ctx context.Context, kind, id string) error {
	m.mu.Lock()
	e := m.entries[key(kind, id)]
	m.mu.Unlock()
	if e == nil {
		return nil
	}
	select {
	case <-e.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	m.mu.Lock()
	select {
	case <-e.done:
		m.mu.Unlock()
		return nil
	default:
	}
	first := e.status.Status != "stopping"
	e.status.Status = "stopping"
	m.mu.Unlock()
	if first {
		if kind == "server" {
			go func() { e.inputMu.Lock(); defer e.inputMu.Unlock(); _, _ = io.WriteString(e.stdin, "stop\n") }()
		} else {
			_ = e.ownership.Kill()
		}
	}
	timer := time.NewTimer(m.stopTimeout)
	defer timer.Stop()
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
	case <-timer.C:
	}
	m.append(e, "Graceful stop timed out; forcing this process tree to close.")
	_ = e.stdin.Close()
	_ = e.ownership.Kill()
	_ = e.command.Process.Kill()
	select {
	case <-e.done:
		return errors.New("process required a forced stop; check the server before restarting")
	case <-time.After(5 * time.Second):
		return errors.New("process did not exit after force-stop")
	}
}
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closing = true
	var statuses []Status
	for _, e := range m.entries {
		statuses = append(statuses, e.status)
	}
	m.mu.Unlock()
	results := make(chan error, len(statuses))
	var wg sync.WaitGroup
	for _, status := range statuses {
		wg.Add(1)
		go func(status Status) { defer wg.Done(); results <- m.Stop(ctx, status.Kind, status.ID) }(status)
	}
	wg.Wait()
	close(results)
	var errs []error
	for err := range results {
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type lineWriter struct {
	mu        sync.Mutex
	buffer    []byte
	truncated bool
	append    func(string)
}

func (w *lineWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range data {
		if b == '\n' {
			w.emit()
			continue
		}
		if len(w.buffer) < 8192 {
			w.buffer = append(w.buffer, b)
		} else {
			w.truncated = true
		}
	}
	return len(data), nil
}
func (w *lineWriter) emit() {
	line := string(w.buffer)
	if w.truncated {
		line = "[oversized line omitted]"
	}
	w.append(line)
	w.buffer = w.buffer[:0]
	w.truncated = false
}
func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buffer) > 0 || w.truncated {
		w.emit()
	}
}
