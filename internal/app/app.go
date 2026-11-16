package app

import (
	"context"
	"github.com/Pekly/NeoLauncher/internal/launcher"
)

// App exposes only local settings/profile operations to the bundled UI.
type App struct {
	ctx   context.Context
	store *launcher.Store
}

func New(dataDir string) (*App, error) {
	store, err := launcher.Open(dataDir)
	if err != nil {
		return nil, err
	}
	return &App{store: store}, nil
}
func (a *App) GetState() launcher.State { return a.store.GetState() }
func (a *App) SaveSettings(settings launcher.Settings) (launcher.State, error) {
	return a.store.SaveSettings(settings)
}
func (a *App) CreateServer(request launcher.CreateServerRequest) (launcher.State, error) {
	return a.store.CreateServer(request)
}
