package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"github.com/Pekly/NeoLauncher/internal/download"
	"github.com/Pekly/NeoLauncher/internal/launcher"
	"github.com/Pekly/NeoLauncher/internal/minecraft"
)

type Operation struct {
	ID       string            `json:"id"`
	TargetID string            `json:"target_id"`
	Kind     string            `json:"kind"`
	Label    string            `json:"label"`
	State    string            `json:"state"`
	Progress download.Progress `json:"progress"`
	Error    string            `json:"error,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
}
type operation struct {
	Operation
	cancel context.CancelFunc
	keys   []string
}

func (a *App) startOperation(kind, id, label string, work func(context.Context, *operation) error) (Operation, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Operation{}, err
	}
	opID := hex.EncodeToString(random[:])
	key := kind + ":" + id
	if id == "" {
		key = "job:" + opID
	}
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return Operation{}, errors.New("launcher is closing")
	}
	running := 0
	for _, op := range a.operations {
		if op.State == "running" {
			running++
		}
	}
	if running >= 3 {
		a.mu.Unlock()
		return Operation{}, errors.New("wait for a download to finish before starting another")
	}
	if a.active[key] || a.runner.Active(kind, id) {
		a.mu.Unlock()
		return Operation{}, errors.New("stop this profile and wait for its current operation first")
	}
	ctx, cancel := context.WithCancel(a.life)
	op := &operation{Operation: Operation{ID: opID, TargetID: id, Kind: kind, Label: label, State: "running", Progress: download.Progress{Stage: "Preparing"}}, cancel: cancel, keys: []string{key}}
	a.active[key] = true
	a.operations = append(a.operations, op)
	a.wg.Add(1)
	for len(a.operations) > 23 {
		removed := false
		for i, old := range a.operations {
			if old.State != "running" {
				a.operations = append(a.operations[:i], a.operations[i+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			break
		}
	}
	result := op.Operation
	a.mu.Unlock()
	go func() {
		var workErr error
		defer func() {
			if recover() != nil {
				workErr = errors.New("operation failed unexpectedly; no running profile was changed")
			}
			cancel()
			a.mu.Lock()
			op.State = "complete"
			if workErr != nil {
				if errors.Is(workErr, context.Canceled) {
					op.State = "cancelled"
				} else {
					op.State = "failed"
					op.Error = workErr.Error()
				}
			}
			for _, key := range op.keys {
				delete(a.active, key)
			}
			a.mu.Unlock()
			a.wg.Done()
		}()
		workErr = work(ctx, op)
	}()
	return result, nil
}
func (a *App) report(op *operation) download.Reporter {
	return func(progress download.Progress) { a.mu.Lock(); op.Progress = progress; a.mu.Unlock() }
}
func (a *App) CancelOperation(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, op := range a.operations {
		if op.ID == id && op.State == "running" {
			op.cancel()
			return
		}
	}
}
func (a *App) InstallProfile(kind, id string) (Operation, error) {
	profile, err := a.store.GetProfile(kind, id)
	if err != nil {
		return Operation{}, err
	}
	if profile.Installed {
		return Operation{}, errors.New("this profile is already installed")
	}
	return a.startOperation(kind, id, "Install "+profile.Name, func(ctx context.Context, op *operation) error {
		profile, err := a.store.GetProfile(kind, id)
		if err != nil {
			return err
		}
		if profile.Pack != nil {
			return a.installPackProfile(ctx, profile, op)
		}
		installed, err := a.installBase(ctx, profile, a.report(op))
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		return a.store.MarkInstalled(kind, id, installed.Version, installed.Loader, installed.LoaderVersion)
	})
}
func (a *App) installBase(ctx context.Context, profile launcher.Target, report download.Reporter) (minecraft.Installation, error) {
	opts := minecraft.Options{Version: profile.Version, Loader: profile.Loader, LoaderVersion: profile.LoaderVersion}
	if _, err := os.Lstat(filepath.Join(profile.Directory, ".neolauncher-install.json")); err == nil {
		installed, err := a.minecraft.ValidateInstallation(profile.Directory)
		if err != nil {
			return minecraft.Installation{}, err
		}
		if installed.Kind != profile.Kind || installed.Version != profile.Version || installed.Loader != profile.Loader || (profile.LoaderVersion != "" && installed.LoaderVersion != profile.LoaderVersion) {
			return minecraft.Installation{}, errors.New("existing installation does not match this profile")
		}
		if report != nil {
			report(download.Progress{Stage: "Base game already installed", Name: profile.Version, Done: 1, Total: 1})
		}
		return installed, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return minecraft.Installation{}, err
	}
	if profile.Kind == "server" {
		return a.minecraft.InstallServer(ctx, profile.Directory, opts, report)
	}
	return a.minecraft.InstallClient(ctx, profile.Directory, opts, report)
}
func (a *App) workDirectory() (string, error) {
	return os.MkdirTemp(filepath.Join(a.root, ".staging"), "install-")
}
