package app

import (
	"errors"
	"github.com/Pekly/NeoLauncher/internal/launcher"
	"github.com/Pekly/NeoLauncher/internal/minecraft"
)

func (a *App) CreateLocalAccount(name string) (AppState, error) {
	done, err := a.reserve("global", "accounts", false)
	if err != nil {
		return AppState{}, err
	}
	defer done()
	if _, err = a.store.CreateLocalAccount(name); err != nil {
		return AppState{}, err
	}
	return a.GetState(), nil
}
func (a *App) SelectLocalAccount(id string) (AppState, error) {
	done, err := a.reserve("global", "accounts", false)
	if err != nil {
		return AppState{}, err
	}
	defer done()
	if err = a.store.SelectLocalAccount(id); err != nil {
		return AppState{}, err
	}
	return a.GetState(), nil
}
func (a *App) RemoveLocalAccount(id string) (AppState, error) {
	done, err := a.reserve("global", "accounts", false)
	if err != nil {
		return AppState{}, err
	}
	defer done()
	if err = a.store.RemoveLocalAccount(id); err != nil {
		return AppState{}, err
	}
	return a.GetState(), nil
}

func (a *App) clientLaunchSpec(profile launcher.Target, state launcher.State) (minecraft.LaunchSpec, error) {
	if state.SelectedAccountKind == "local" {
		for _, account := range state.LocalAccounts {
			if account.ID == state.SelectedLocalAccountID {
				return a.minecraft.LocalClientLaunch(profile.Directory, state.Settings.JavaPath, state.Settings.ClientMemoryMB, account.Name)
			}
		}
		return minecraft.LaunchSpec{}, errors.New("select a local player first")
	}
	if state.SelectedAccountKind != "" && state.SelectedAccountKind != "microsoft" {
		return minecraft.LaunchSpec{}, errors.New("select a Local or Microsoft player first")
	}
	if state.SelectedAccountID == "" {
		return minecraft.LaunchSpec{}, errors.New("sign in with a Microsoft account first, or explicitly select a local player")
	}
	session, err := a.accounts.Session(a.life, state.SelectedAccountID)
	if err != nil {
		return minecraft.LaunchSpec{}, err
	}
	// Authentication errors never fall back to a different/local player identity.
	return a.minecraft.ClientLaunch(profile.Directory, state.Settings.JavaPath, state.Settings.ClientMemoryMB, minecraft.Identity{Name: session.Name, UUID: session.UUID, AccessToken: session.AccessToken, XUID: session.XUID})
}
