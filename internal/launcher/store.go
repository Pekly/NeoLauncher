package launcher

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Settings struct {
	JavaPath       string `json:"java_path"`
	ClientMemoryMB int    `json:"client_memory_mb"`
}
type ServerProfile struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Port      int       `json:"port"`
	MemoryMB  int       `json:"memory_mb"`
	Directory string    `json:"directory"`
	CreatedAt time.Time `json:"created_at"`
}
type State struct {
	SchemaVersion  int             `json:"schema_version"`
	DataDir        string          `json:"data_dir"`
	Settings       Settings        `json:"settings"`
	Servers        []ServerProfile `json:"servers"`
	NextServerPort int             `json:"next_server_port"`
}
type CreateServerRequest struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Port     int    `json:"port"`
	MemoryMB int    `json:"memory_mb"`
}
type Store struct {
	mu    sync.Mutex
	root  string
	state State
}

var versionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+\-]{0,47}$`)
var profileID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func Open(root string) (*Store, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Join(absolute, "servers"), 0700); err != nil {
		return nil, err
	}
	s := &Store{root: absolute, state: State{SchemaVersion: 1, Settings: Settings{ClientMemoryMB: 4096}, Servers: []ServerProfile{}}}
	file, err := os.Open(filepath.Join(absolute, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		if err = s.save(s.state); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("settings file is too large")
	}
	if err = json.Unmarshal(data, &s.state); err != nil {
		return nil, fmt.Errorf("could not read state.json; restore a backup rather than deleting your server folders: %w", err)
	}
	if s.state.SchemaVersion != 1 {
		return nil, errors.New("unsupported settings version")
	}
	if err = validateSettings(s.state.Settings); err != nil {
		return nil, err
	}
	if len(s.state.Servers) > 1000 {
		return nil, errors.New("too many server profiles")
	}
	ids, ports := map[string]bool{}, map[int]bool{}
	for i := range s.state.Servers {
		p := &s.state.Servers[i]
		if !profileID.MatchString(p.ID) || ids[p.ID] || ports[p.Port] {
			return nil, errors.New("invalid or duplicate server profile")
		}
		if err = validateServer(CreateServerRequest{p.Name, p.Version, p.Port, p.MemoryMB}); err != nil {
			return nil, err
		}
		p.Directory = s.directory(p.ID)
		ids[p.ID] = true
		ports[p.Port] = true
	}
	return s, nil
}
func memoryValid(value int) bool { return value >= 512 && value <= 32768 }
func validateSettings(settings Settings) error {
	if !memoryValid(settings.ClientMemoryMB) {
		return errors.New("choose between 512 and 32768 MB of memory")
	}
	if len(settings.JavaPath) > 4096 || strings.ContainsAny(settings.JavaPath, "\x00\r\n") {
		return errors.New("invalid Java path")
	}
	return nil
}
func validateServer(req CreateServerRequest) error {
	if strings.TrimSpace(req.Name) == "" || utf8.RuneCountInString(req.Name) > 60 || strings.ContainsAny(req.Name, "\x00\r\n") {
		return errors.New("use a server name of 1–60 characters")
	}
	if req.Version != "" && !versionID.MatchString(req.Version) {
		return errors.New("use a Minecraft version such as 1.21.1, or leave it blank")
	}
	if req.Port < 1024 || req.Port > 65535 {
		return errors.New("choose a port between 1024 and 65535")
	}
	if !memoryValid(req.MemoryMB) {
		return errors.New("choose between 512 and 32768 MB of server memory")
	}
	return nil
}
func (s *Store) directory(id string) string { return filepath.Join(s.root, "servers", id) }
func (s *Store) snapshot() State {
	state := s.state
	state.DataDir = s.root
	state.Servers = append([]ServerProfile{}, s.state.Servers...)
	used := map[int]bool{}
	for _, p := range state.Servers {
		used[p.Port] = true
	}
	state.NextServerPort = 25565
	for used[state.NextServerPort] {
		state.NextServerPort++
		if state.NextServerPort > 65535 {
			state.NextServerPort = 1024
		}
	}
	return state
}
func (s *Store) GetState() State { s.mu.Lock(); defer s.mu.Unlock(); return s.snapshot() }
func (s *Store) SaveSettings(settings Settings) (State, error) {
	settings.JavaPath = strings.TrimSpace(settings.JavaPath)
	if err := validateSettings(settings); err != nil {
		return State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.state
	next.Settings = settings
	if err := s.save(next); err != nil {
		return State{}, err
	}
	s.state = next
	return s.snapshot(), nil
}
func (s *Store) CreateServer(req CreateServerRequest) (State, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Version = strings.TrimSpace(req.Version)
	if err := validateServer(req); err != nil {
		return State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.state.Servers) >= 1000 {
		return State{}, errors.New("server profile limit reached")
	}
	for _, p := range s.state.Servers {
		if p.Port == req.Port {
			return State{}, errors.New("that port is already used by another profile")
		}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return State{}, err
	}
	id := hex.EncodeToString(random[:])
	directory := s.directory(id)
	if err := os.Mkdir(directory, 0700); err != nil {
		return State{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(directory)
		}
	}()
	files := map[string]string{
		"server.properties": fmt.Sprintf("# Created by NeoLauncher. No server has been installed or started.\nserver-port=%d\nonline-mode=true\nenable-rcon=false\nmax-players=20\nmotd=NeoLauncher server\n", req.Port),
		"eula.txt":          "# Review the Minecraft EULA before running a server.\n# https://aka.ms/MinecraftEULA\neula=false\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0600); err != nil {
			return State{}, err
		}
	}
	profile := ServerProfile{id, req.Name, req.Version, req.Port, req.MemoryMB, directory, time.Now().UTC()}
	next := s.state
	next.Servers = append(append([]ServerProfile{}, next.Servers...), profile)
	if err := s.save(next); err != nil {
		return State{}, err
	}
	s.state = next
	committed = true
	return s.snapshot(), nil
}
func (s *Store) ServerDirectory(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.state.Servers {
		if p.ID == id {
			return s.directory(p.ID), nil
		}
	}
	return "", errors.New("server profile not found")
}
func (s *Store) save(state State) error {
	state.DataDir = ""
	state.NextServerPort = 0
	state.Servers = append([]ServerProfile{}, state.Servers...)
	for i := range state.Servers {
		state.Servers[i].Directory = ""
	} // Derived, never trusted from disk.
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > 1<<20 {
		return errors.New("settings file is too large")
	}
	file, err := os.CreateTemp(s.root, ".state-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(append(data, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, filepath.Join(s.root, "state.json"))
}
