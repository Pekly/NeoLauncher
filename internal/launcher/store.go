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
	JavaPath          string `json:"java_path"`
	ClientMemoryMB    int    `json:"client_memory_mb"`
	CurseForgeEnabled bool   `json:"curseforge_enabled"`
}
type PackSource struct {
	Provider        string `json:"provider"`
	ProjectID       string `json:"project_id"`
	VersionID       string `json:"version_id"`
	IncludeOptional bool   `json:"include_optional"`
}
type ServerProfile struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Version       string      `json:"version"`
	Port          int         `json:"port"`
	MemoryMB      int         `json:"memory_mb"`
	Directory     string      `json:"directory"`
	CreatedAt     time.Time   `json:"created_at"`
	Loader        string      `json:"loader"`
	LoaderVersion string      `json:"loader_version"`
	Installed     bool        `json:"installed"`
	Pack          *PackSource `json:"pack,omitempty"`
}
type InstanceProfile struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Version       string      `json:"version"`
	Loader        string      `json:"loader"`
	LoaderVersion string      `json:"loader_version"`
	Directory     string      `json:"directory"`
	CreatedAt     time.Time   `json:"created_at"`
	Installed     bool        `json:"installed"`
	Pack          *PackSource `json:"pack,omitempty"`
}
type State struct {
	SchemaVersion      int               `json:"schema_version"`
	DataDir            string            `json:"data_dir"`
	Settings           Settings          `json:"settings"`
	Servers            []ServerProfile   `json:"servers"`
	Instances          []InstanceProfile `json:"instances"`
	NextServerPort     int               `json:"next_server_port"`
	SelectedAccountID  string            `json:"selected_account_id"`
	SelectedInstanceID string            `json:"selected_instance_id"`
}
type CreateServerRequest struct {
	Name     string      `json:"name"`
	Version  string      `json:"version"`
	Port     int         `json:"port"`
	MemoryMB int         `json:"memory_mb"`
	Loader   string      `json:"loader"`
	Pack     *PackSource `json:"-"`
}
type CreateInstanceRequest struct {
	Name    string      `json:"name"`
	Version string      `json:"version"`
	Loader  string      `json:"loader"`
	Pack    *PackSource `json:"-"`
}
type Target struct {
	ID, Kind, Name, Version, Loader, LoaderVersion, Directory string
	Port, MemoryMB                                            int
	Installed                                                 bool
	Pack                                                      *PackSource
}
type Store struct {
	mu    sync.Mutex
	root  string
	state State
}

var versionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._+\-]{0,63}$`)
var profileID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func normalizedLoader(loader string) string {
	loader = strings.ToLower(strings.TrimSpace(loader))
	if loader == "" {
		return "vanilla"
	}
	return loader
}
func validateLoader(loader string) error {
	if normalizedLoader(loader) != "vanilla" && normalizedLoader(loader) != "fabric" {
		return errors.New("only Vanilla and Fabric are supported")
	}
	return nil
}
func copyPack(p *PackSource) *PackSource {
	if p == nil {
		return nil
	}
	copy := *p
	return &copy
}
func clone(state State) State {
	state.Servers = append([]ServerProfile{}, state.Servers...)
	state.Instances = append([]InstanceProfile{}, state.Instances...)
	for i := range state.Servers {
		state.Servers[i].Pack = copyPack(state.Servers[i].Pack)
	}
	for i := range state.Instances {
		state.Instances[i].Pack = copyPack(state.Instances[i].Pack)
	}
	return state
}
func Open(root string) (*Store, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for _, directory := range []string{"servers", "instances", "cache", ".staging"} {
		if err = os.MkdirAll(filepath.Join(absolute, directory), 0700); err != nil {
			return nil, err
		}
	}
	s := &Store{root: absolute, state: State{SchemaVersion: 1, Settings: Settings{ClientMemoryMB: 4096}, Servers: []ServerProfile{}, Instances: []InstanceProfile{}}}
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
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("settings file is too large")
	}
	if err = json.Unmarshal(data, &s.state); err != nil {
		return nil, fmt.Errorf("could not read state.json; restore a backup rather than deleting server folders: %w", err)
	}
	if s.state.SchemaVersion != 1 {
		return nil, errors.New("unsupported settings version")
	}
	if err = validateSettings(s.state.Settings); err != nil {
		return nil, err
	}
	if len(s.state.Servers)+len(s.state.Instances) > 1000 {
		return nil, errors.New("too many profiles")
	}
	ids, ports := map[string]bool{}, map[int]bool{}
	for i := range s.state.Servers {
		p := &s.state.Servers[i]
		if !profileID.MatchString(p.ID) || ids[p.ID] || ports[p.Port] {
			return nil, errors.New("invalid or duplicate server profile")
		}
		if err = validateServer(CreateServerRequest{Name: p.Name, Version: p.Version, Port: p.Port, MemoryMB: p.MemoryMB, Loader: p.Loader}); err != nil {
			return nil, err
		}
		p.Loader = normalizedLoader(p.Loader)
		p.Directory = s.directory("server", p.ID)
		ids[p.ID] = true
		ports[p.Port] = true
	}
	for i := range s.state.Instances {
		p := &s.state.Instances[i]
		if !profileID.MatchString(p.ID) || ids[p.ID] {
			return nil, errors.New("invalid or duplicate game profile")
		}
		if err = validateInstance(CreateInstanceRequest{Name: p.Name, Version: p.Version, Loader: p.Loader}); err != nil {
			return nil, err
		}
		p.Loader = normalizedLoader(p.Loader)
		p.Directory = s.directory("client", p.ID)
		ids[p.ID] = true
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
func validateNameVersion(name, version string) error {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 60 || strings.ContainsAny(name, "\x00\r\n") {
		return errors.New("use a profile name of 1–60 characters")
	}
	if version != "" && !versionID.MatchString(version) {
		return errors.New("invalid Minecraft version")
	}
	return nil
}
func validateServer(req CreateServerRequest) error {
	if err := validateNameVersion(req.Name, req.Version); err != nil {
		return err
	}
	if err := validateLoader(req.Loader); err != nil {
		return err
	}
	if req.Port < 1024 || req.Port > 65535 {
		return errors.New("choose a port between 1024 and 65535")
	}
	if !memoryValid(req.MemoryMB) {
		return errors.New("choose between 512 and 32768 MB of server memory")
	}
	return nil
}
func validateInstance(req CreateInstanceRequest) error {
	if err := validateNameVersion(req.Name, req.Version); err != nil {
		return err
	}
	if req.Version == "" {
		return errors.New("choose a Minecraft version")
	}
	return validateLoader(req.Loader)
}
func (s *Store) directory(kind, id string) string {
	folder := "servers"
	if kind == "client" {
		folder = "instances"
	}
	return filepath.Join(s.root, folder, id)
}
func (s *Store) snapshot() State {
	state := clone(s.state)
	state.DataDir = s.root
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
	next := clone(s.state)
	next.Settings = settings
	if err := s.save(next); err != nil {
		return State{}, err
	}
	s.state = next
	return s.snapshot(), nil
}
func newID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(random[:]), nil
}
func (s *Store) CreateServer(req CreateServerRequest) (State, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Version = strings.TrimSpace(req.Version)
	req.Loader = normalizedLoader(req.Loader)
	if err := validateServer(req); err != nil {
		return State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.state.Servers)+len(s.state.Instances) >= 1000 {
		return State{}, errors.New("profile limit reached")
	}
	for _, p := range s.state.Servers {
		if p.Port == req.Port {
			return State{}, errors.New("that port is already used by another profile")
		}
	}
	id, err := newID()
	if err != nil {
		return State{}, err
	}
	directory := s.directory("server", id)
	if err = os.Mkdir(directory, 0700); err != nil {
		return State{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(directory)
		}
	}()
	files := map[string]string{"server.properties": fmt.Sprintf("server-port=%d\nonline-mode=true\nenable-rcon=false\nmax-players=20\nmotd=NeoLauncher server\n", req.Port), "eula.txt": "# Review https://aka.ms/MinecraftEULA before starting.\neula=false\n"}
	for name, text := range files {
		if err = os.WriteFile(filepath.Join(directory, name), []byte(text), 0600); err != nil {
			return State{}, err
		}
	}
	next := clone(s.state)
	next.Servers = append(next.Servers, ServerProfile{ID: id, Name: req.Name, Version: req.Version, Port: req.Port, MemoryMB: req.MemoryMB, Directory: directory, CreatedAt: time.Now().UTC(), Loader: req.Loader, Pack: copyPack(req.Pack)})
	if err = s.save(next); err != nil {
		return State{}, err
	}
	s.state = next
	committed = true
	return s.snapshot(), nil
}
func (s *Store) CreateInstance(req CreateInstanceRequest) (State, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Version = strings.TrimSpace(req.Version)
	req.Loader = normalizedLoader(req.Loader)
	if err := validateInstance(req); err != nil {
		return State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.state.Servers)+len(s.state.Instances) >= 1000 {
		return State{}, errors.New("profile limit reached")
	}
	id, err := newID()
	if err != nil {
		return State{}, err
	}
	directory := s.directory("client", id)
	if err = os.Mkdir(directory, 0700); err != nil {
		return State{}, err
	}
	next := clone(s.state)
	next.Instances = append(next.Instances, InstanceProfile{ID: id, Name: req.Name, Version: req.Version, Loader: req.Loader, Directory: directory, CreatedAt: time.Now().UTC(), Pack: copyPack(req.Pack)})
	next.SelectedInstanceID = id
	if err = s.save(next); err != nil {
		_ = os.RemoveAll(directory)
		return State{}, err
	}
	s.state = next
	return s.snapshot(), nil
}
func (s *Store) GetProfile(kind, id string) (Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind == "server" {
		for _, p := range s.state.Servers {
			if p.ID == id {
				return Target{p.ID, kind, p.Name, p.Version, p.Loader, p.LoaderVersion, s.directory(kind, id), p.Port, p.MemoryMB, p.Installed, copyPack(p.Pack)}, nil
			}
		}
	}
	if kind == "client" {
		for _, p := range s.state.Instances {
			if p.ID == id {
				return Target{p.ID, kind, p.Name, p.Version, p.Loader, p.LoaderVersion, s.directory(kind, id), 0, s.state.Settings.ClientMemoryMB, p.Installed, copyPack(p.Pack)}, nil
			}
		}
	}
	return Target{}, errors.New("profile not found")
}
func (s *Store) ServerDirectory(id string) (string, error) {
	p, err := s.GetProfile("server", id)
	return p.Directory, err
}
func (s *Store) InstanceDirectory(id string) (string, error) {
	p, err := s.GetProfile("client", id)
	return p.Directory, err
}
func (s *Store) SetProfileVersion(kind, id, version, loader string) error {
	if version == "" || !versionID.MatchString(version) {
		return errors.New("choose a Minecraft version")
	}
	if err := validateLoader(loader); err != nil {
		return err
	}
	loader = normalizedLoader(loader)
	return s.modifyProfile(kind, id, func(server *ServerProfile, instance *InstanceProfile) error {
		if server != nil {
			if server.Installed || server.Pack != nil {
				return errors.New("create a new profile to change an installed game or modpack")
			}
			server.Version = version
			server.Loader = loader
			server.LoaderVersion = ""
		}
		if instance != nil {
			if instance.Installed || instance.Pack != nil {
				return errors.New("create a new profile to change an installed game or modpack")
			}
			instance.Version = version
			instance.Loader = loader
			instance.LoaderVersion = ""
		}
		return nil
	})
}
func (s *Store) MarkInstalled(kind, id, version, loader, loaderVersion string) error {
	if !versionID.MatchString(version) {
		return errors.New("invalid installed version")
	}
	if err := validateLoader(loader); err != nil {
		return err
	}
	return s.modifyProfile(kind, id, func(server *ServerProfile, instance *InstanceProfile) error {
		if server != nil {
			server.Version = version
			server.Loader = normalizedLoader(loader)
			server.LoaderVersion = loaderVersion
			server.Installed = true
		}
		if instance != nil {
			instance.Version = version
			instance.Loader = normalizedLoader(loader)
			instance.LoaderVersion = loaderVersion
			instance.Installed = true
		}
		return nil
	})
}
func (s *Store) SetPack(kind, id string, pack PackSource) error {
	if (pack.Provider != "modrinth" && pack.Provider != "curseforge") || pack.ProjectID == "" || pack.VersionID == "" || len(pack.ProjectID) > 128 || len(pack.VersionID) > 128 {
		return errors.New("invalid modpack source")
	}
	return s.modifyProfile(kind, id, func(server *ServerProfile, instance *InstanceProfile) error {
		if server != nil {
			server.Pack = copyPack(&pack)
		}
		if instance != nil {
			instance.Pack = copyPack(&pack)
		}
		return nil
	})
}
func (s *Store) modifyProfile(kind, id string, change func(*ServerProfile, *InstanceProfile) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.state)
	found := false
	if kind == "server" {
		for i := range next.Servers {
			if next.Servers[i].ID == id {
				if err := change(&next.Servers[i], nil); err != nil {
					return err
				}
				found = true
				break
			}
		}
	}
	if kind == "client" {
		for i := range next.Instances {
			if next.Instances[i].ID == id {
				if err := change(nil, &next.Instances[i]); err != nil {
					return err
				}
				found = true
				break
			}
		}
	}
	if !found {
		return errors.New("profile not found")
	}
	if err := s.save(next); err != nil {
		return err
	}
	s.state = next
	return nil
}
func (s *Store) SelectAccount(id string) error {
	if id != "" && !profileID.MatchString(strings.ReplaceAll(id, "-", "")) {
		return errors.New("invalid account identifier")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.state)
	next.SelectedAccountID = id
	if err := s.save(next); err != nil {
		return err
	}
	s.state = next
	return nil
}
func (s *Store) SelectInstance(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := id == ""
	for _, p := range s.state.Instances {
		if p.ID == id {
			found = true
		}
	}
	if !found {
		return errors.New("profile not found")
	}
	next := clone(s.state)
	next.SelectedInstanceID = id
	if err := s.save(next); err != nil {
		return err
	}
	s.state = next
	return nil
}
func (s *Store) save(state State) error {
	state = clone(state)
	state.DataDir = ""
	state.NextServerPort = 0
	for i := range state.Servers {
		state.Servers[i].Directory = ""
	}
	for i := range state.Instances {
		state.Instances[i].Directory = ""
	}
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
