package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Pekly/NeoLauncher/internal/accounts"
	"github.com/Pekly/NeoLauncher/internal/content"
	"github.com/Pekly/NeoLauncher/internal/download"
	"github.com/Pekly/NeoLauncher/internal/launcher"
	"github.com/Pekly/NeoLauncher/internal/minecraft"
	"github.com/Pekly/NeoLauncher/internal/runner"
)

const MicrosoftClientID = "af8942e2-c4a6-46b3-9085-5c657293f1d0"

type App struct {
	ctx                                    context.Context
	life                                   context.Context
	cancel                                 context.CancelFunc
	root                                   string
	store                                  *launcher.Store
	accounts                               *accounts.Service
	downloads                              *download.Client
	minecraft                              *minecraft.Service
	content                                *content.Service
	runner                                 *runner.Manager
	mu                                     sync.Mutex
	active                                 map[string]bool
	operations                             []*operation
	wg                                     sync.WaitGroup
	closing, closed, prompt, keyConfigured bool
	closeOnce                              sync.Once
	closeErr                               error
}
type AppState struct {
	launcher.State
	Accounts             []accounts.Account `json:"accounts"`
	AccountsError        string             `json:"accounts_error,omitempty"`
	CurseForgeConfigured bool               `json:"curseforge_configured"`
	AuthClientID         string             `json:"auth_client_id"`
	Operations           []Operation        `json:"operations"`
	Processes            []runner.Status    `json:"processes"`
	Closing              bool               `json:"closing"`
}

func New(dataDir string) (*App, error) {
	store, err := launcher.Open(dataDir)
	if err != nil {
		return nil, err
	}
	root := store.GetState().DataDir
	auth, err := accounts.NewService(root, MicrosoftClientID)
	if err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	a := &App{root: root, store: store, accounts: auth, downloads: download.New(), runner: runner.New(), life: life, cancel: cancel, active: map[string]bool{}}
	if key, err := auth.GetSecret("curseforge-api-key"); err == nil && key != "" {
		a.keyConfigured = true
	}
	a.minecraft = minecraft.New(root, a.downloads)
	a.content = content.New(a.downloads, func() (string, error) { return a.accounts.GetSecret("curseforge-api-key") }, func() bool { return a.store.GetState().Settings.CurseForgeEnabled && a.hasKey() })
	return a, nil
}
func (a *App) hasKey() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.keyConfigured }
func (a *App) GetState() AppState {
	state := AppState{State: a.store.GetState(), AuthClientID: MicrosoftClientID, Accounts: []accounts.Account{}, Operations: []Operation{}}
	a.mu.Lock()
	state.CurseForgeConfigured = a.keyConfigured
	state.Closing = a.closing
	for _, op := range a.operations {
		state.Operations = append(state.Operations, op.Operation)
	}
	a.mu.Unlock()
	if !state.CurseForgeConfigured {
		state.Settings.CurseForgeEnabled = false
	}
	if a.accounts != nil {
		items, err := a.accounts.List()
		if err != nil {
			state.AccountsError = err.Error()
		} else if items != nil {
			state.Accounts = items
		}
	}
	state.Processes = a.runner.Statuses()
	return state
}
func (a *App) reserve(kind, id string, requireStopped bool) (func(), error) {
	key := kind + ":" + id
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return nil, errors.New("launcher is closing")
	}
	if a.active[key] {
		return nil, errors.New("an operation is already running for this profile")
	}
	if requireStopped && a.runner.Active(kind, id) {
		return nil, errors.New("stop the game or server before changing its files")
	}
	a.active[key] = true
	a.wg.Add(1)
	return func() { a.mu.Lock(); delete(a.active, key); a.mu.Unlock(); a.wg.Done() }, nil
}
func (a *App) SaveSettings(settings launcher.Settings) (AppState, error) {
	done, err := a.reserve("global", "settings", false)
	if err != nil {
		return AppState{}, err
	}
	defer done()
	if settings.CurseForgeEnabled && !a.hasKey() {
		return AppState{}, errors.New("add a CurseForge API key before enabling CurseForge")
	}
	if _, err = a.store.SaveSettings(settings); err != nil {
		return AppState{}, err
	}
	return a.GetState(), nil
}
func (a *App) CreateServer(request launcher.CreateServerRequest) (AppState, error) {
	done, err := a.reserve("global", "profiles", false)
	if err != nil {
		return AppState{}, err
	}
	defer done()
	if _, err = a.store.CreateServer(request); err != nil {
		return AppState{}, err
	}
	return a.GetState(), nil
}
func (a *App) CreateInstance(request launcher.CreateInstanceRequest) (AppState, error) {
	done, err := a.reserve("global", "profiles", false)
	if err != nil {
		return AppState{}, err
	}
	defer done()
	if _, err = a.store.CreateInstance(request); err != nil {
		return AppState{}, err
	}
	return a.GetState(), nil
}
func (a *App) SetProfileVersion(kind, id, version, loader string) (AppState, error) {
	done, err := a.reserve(kind, id, true)
	if err != nil {
		return AppState{}, err
	}
	defer done()
	if err = a.store.SetProfileVersion(kind, id, version, loader); err != nil {
		return AppState{}, err
	}
	return a.GetState(), nil
}
func (a *App) GetMinecraftVersions() ([]minecraft.Version, error) {
	return a.minecraft.Versions(a.life)
}
func (a *App) BeginMicrosoftLogin() (accounts.DeviceLogin, error) { return a.accounts.Begin(a.life) }
func (a *App) PollMicrosoftLogin(id string) (accounts.LoginResult, error) {
	result, err := a.accounts.Poll(a.life, id)
	if err == nil && result.Status == "complete" && result.Account != nil {
		err = a.store.SelectAccount(result.Account.ID)
	}
	return result, err
}
func (a *App) CancelMicrosoftLogin(id string) { a.accounts.Cancel(id) }
func (a *App) SelectAccount(id string) (AppState, error) {
	list, err := a.accounts.List()
	if err != nil {
		return AppState{}, err
	}
	found := false
	for _, account := range list {
		if account.ID == id {
			found = true
		}
	}
	if !found {
		return AppState{}, errors.New("account not found")
	}
	if err = a.store.SelectAccount(id); err != nil {
		return AppState{}, err
	}
	return a.GetState(), nil
}
func (a *App) RemoveAccount(id string) (AppState, error) {
	if err := a.accounts.Remove(id); err != nil {
		return AppState{}, err
	}
	if a.store.GetState().SelectedAccountID == id {
		if err := a.store.SelectAccount(""); err != nil {
			return AppState{}, err
		}
	}
	return a.GetState(), nil
}
func (a *App) SetCurseForgeKey(key string) (AppState, error) {
	done, err := a.reserve("global", "settings", false)
	if err != nil {
		return AppState{}, err
	}
	defer done()
	key = strings.TrimSpace(key)
	if len(key) > 4096 || strings.ContainsAny(key, "\x00\r\n") {
		return AppState{}, errors.New("invalid CurseForge API key")
	}
	if key == "" {
		err = a.accounts.DeleteSecret("curseforge-api-key")
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else {
		err = a.accounts.SetSecret("curseforge-api-key", key)
	}
	if err != nil {
		return AppState{}, err
	}
	a.mu.Lock()
	a.keyConfigured = key != ""
	a.mu.Unlock()
	if key == "" {
		settings := a.store.GetState().Settings
		settings.CurseForgeEnabled = false
		if _, err = a.store.SaveSettings(settings); err != nil {
			return AppState{}, err
		}
	}
	return a.GetState(), nil
}
func (a *App) StartServer(id string, acceptEULA bool) (AppState, error) {
	done, err := a.reserve("server", id, true)
	if err != nil {
		return AppState{}, err
	}
	defer done()
	profile, err := a.store.GetProfile("server", id)
	if err != nil {
		return AppState{}, err
	}
	if !profile.Installed {
		return AppState{}, errors.New("install this server first")
	}
	if acceptEULA {
		if err = a.minecraft.AcceptEULA(profile.Directory); err != nil {
			return AppState{}, err
		}
	}
	spec, err := a.minecraft.ServerLaunch(profile.Directory, a.store.GetState().Settings.JavaPath, profile.MemoryMB)
	if err != nil {
		return AppState{}, err
	}
	if err = a.runner.Start("server", id, runner.Spec{Program: spec.Program, Args: spec.Args, Directory: spec.Directory, Secrets: spec.Secrets}, profile.MemoryMB); err != nil {
		return AppState{}, err
	}
	return a.GetState(), nil
}
func (a *App) StartClient(id string) (AppState, error) {
	done, err := a.reserve("client", id, true)
	if err != nil {
		return AppState{}, err
	}
	defer done()
	profile, err := a.store.GetProfile("client", id)
	if err != nil {
		return AppState{}, err
	}
	if !profile.Installed {
		return AppState{}, errors.New("install this instance first")
	}
	state := a.store.GetState()
	if state.SelectedAccountID == "" {
		return AppState{}, errors.New("sign in with a Microsoft account first")
	}
	session, err := a.accounts.Session(a.life, state.SelectedAccountID)
	if err != nil {
		return AppState{}, err
	}
	spec, err := a.minecraft.ClientLaunch(profile.Directory, state.Settings.JavaPath, state.Settings.ClientMemoryMB, minecraft.Identity{Name: session.Name, UUID: session.UUID, AccessToken: session.AccessToken, XUID: session.XUID})
	if err != nil {
		return AppState{}, err
	}
	if err = a.runner.Start("client", id, runner.Spec{Program: spec.Program, Args: spec.Args, Directory: spec.Directory, Secrets: append(spec.Secrets, session.AccessToken)}, state.Settings.ClientMemoryMB); err != nil {
		return AppState{}, err
	}
	_ = a.store.SelectInstance(id)
	return a.GetState(), nil
}
func (a *App) StopServer(id string) (AppState, error) { return a.stop("server", id) }
func (a *App) StopClient(id string) (AppState, error) { return a.stop("client", id) }
func (a *App) stop(kind, id string) (AppState, error) {
	done, err := a.reserve(kind, id, false)
	if err != nil {
		return AppState{}, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	err = a.runner.Stop(ctx, kind, id)
	return a.GetState(), err
}
func (a *App) SendServerCommand(id, command string) error {
	if _, err := a.store.GetProfile("server", id); err != nil {
		return err
	}
	return a.runner.Send(id, command)
}
func (a *App) GetLogs(kind, id string) []string { return a.runner.Logs(kind, id) }
func (a *App) closeServices() error {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closing = true
		a.mu.Unlock()
		a.cancel()
		a.wg.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		a.closeErr = a.runner.Shutdown(ctx)
		a.accounts.Close()
	})
	return a.closeErr
}
