package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Pekly/NeoLauncher/internal/content"
	"github.com/Pekly/NeoLauncher/internal/download"
	"github.com/Pekly/NeoLauncher/internal/launcher"
)

type ContentVersionsRequest struct {
	Provider    string `json:"provider"`
	ProjectID   string `json:"project_id"`
	GameVersion string `json:"game_version"`
	Loader      string `json:"loader"`
}
type ContentInstallRequest struct {
	Provider        string `json:"provider"`
	Kind            string `json:"kind"`
	ProjectID       string `json:"project_id"`
	VersionID       string `json:"version_id"`
	TargetKind      string `json:"target_kind"`
	TargetID        string `json:"target_id"`
	Name            string `json:"name"`
	Port            int    `json:"port"`
	MemoryMB        int    `json:"memory_mb"`
	IncludeOptional bool   `json:"include_optional"`
}

func (a *App) SearchContent(request content.SearchRequest) ([]content.Project, error) {
	return a.content.Search(a.life, request)
}
func (a *App) GetContentVersions(request ContentVersionsRequest) ([]content.Version, error) {
	return a.content.Versions(a.life, request.Provider, request.ProjectID, request.GameVersion, request.Loader)
}
func (a *App) InstallContent(request ContentInstallRequest) (Operation, error) {
	if request.TargetKind != "client" && request.TargetKind != "server" {
		return Operation{}, errors.New("choose client or server")
	}
	if request.Kind != "mod" && request.Kind != "modpack" {
		return Operation{}, errors.New("choose a mod or modpack")
	}
	if request.Kind == "mod" {
		profile, err := a.store.GetProfile(request.TargetKind, request.TargetID)
		if err != nil {
			return Operation{}, err
		}
		return a.startOperation(request.TargetKind, request.TargetID, "Install mod", func(ctx context.Context, op *operation) error {
			profile, err = a.store.GetProfile(request.TargetKind, request.TargetID)
			if err != nil {
				return err
			}
			if !profile.Installed || profile.Loader != "fabric" {
				return errors.New("install a Fabric profile before adding mods")
			}
			work, err := a.workDirectory()
			if err != nil {
				return err
			}
			defer os.RemoveAll(work)
			plan, err := a.content.Plan(ctx, content.InstallRequest{Provider: request.Provider, Kind: "mod", ProjectID: request.ProjectID, VersionID: request.VersionID, GameVersion: profile.Version, Loader: profile.Loader, Side: profile.Kind, IncludeOptional: request.IncludeOptional}, work, a.report(op))
			if err != nil {
				return err
			}
			if err = a.publishContent(ctx, profile, plan, work, a.report(op)); err != nil {
				return err
			}
			a.mu.Lock()
			op.Warnings = append([]string{}, plan.Warnings...)
			a.mu.Unlock()
			return nil
		})
	}
	if request.TargetID != "" {
		return Operation{}, errors.New("modpacks must use a new profile")
	}
	return a.startOperation(request.TargetKind, "", "Install modpack", func(ctx context.Context, op *operation) error {
		work, err := a.workDirectory()
		if err != nil {
			return err
		}
		defer os.RemoveAll(work)
		plan, err := a.content.Plan(ctx, content.InstallRequest{Provider: request.Provider, Kind: "modpack", ProjectID: request.ProjectID, VersionID: request.VersionID, Side: request.TargetKind, IncludeOptional: request.IncludeOptional}, work, a.report(op))
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		name := strings.TrimSpace(request.Name)
		if name == "" {
			name = plan.Name
			if chars := []rune(name); len(chars) > 60 {
				name = string(chars[:60])
			}
		}
		pack := &launcher.PackSource{Provider: request.Provider, ProjectID: request.ProjectID, VersionID: request.VersionID, IncludeOptional: request.IncludeOptional}
		a.mu.Lock()
		var state launcher.State
		if request.TargetKind == "server" {
			port, memory := request.Port, request.MemoryMB
			if port == 0 {
				port = a.store.GetState().NextServerPort
			}
			if memory == 0 {
				memory = 2048
			}
			state, err = a.store.CreateServer(launcher.CreateServerRequest{Name: name, Version: plan.Version, Loader: plan.Loader, Port: port, MemoryMB: memory, Pack: pack})
			if err == nil {
				op.TargetID = state.Servers[len(state.Servers)-1].ID
			}
		} else {
			state, err = a.store.CreateInstance(launcher.CreateInstanceRequest{Name: name, Version: plan.Version, Loader: plan.Loader, Pack: pack})
			if err == nil {
				op.TargetID = state.Instances[len(state.Instances)-1].ID
			}
		}
		if err == nil {
			key := request.TargetKind + ":" + op.TargetID
			a.active[key] = true
			op.keys = append(op.keys, key)
			op.Label = "Install " + name
		}
		a.mu.Unlock()
		if err != nil {
			return err
		}
		profile, err := a.store.GetProfile(request.TargetKind, op.TargetID)
		if err != nil {
			return err
		}
		profile.LoaderVersion = plan.LoaderVersion
		return a.finishPack(ctx, profile, plan, work, op)
	})
}
func (a *App) installPackProfile(ctx context.Context, profile launcher.Target, op *operation) error {
	work, err := a.workDirectory()
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	source := profile.Pack
	plan, err := a.content.Plan(ctx, content.InstallRequest{Provider: source.Provider, Kind: "modpack", ProjectID: source.ProjectID, VersionID: source.VersionID, Side: profile.Kind, IncludeOptional: source.IncludeOptional}, work, a.report(op))
	if err != nil {
		return err
	}
	if plan.Version != profile.Version || plan.Loader != profile.Loader {
		return errors.New("modpack metadata changed; create a new profile")
	}
	profile.LoaderVersion = plan.LoaderVersion
	return a.finishPack(ctx, profile, plan, work, op)
}
func (a *App) finishPack(ctx context.Context, profile launcher.Target, plan *content.Plan, work string, op *operation) error {
	installed, err := a.installBase(ctx, profile, a.report(op))
	if err != nil {
		return err
	}
	if err = a.publishContent(ctx, profile, plan, work, a.report(op)); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = a.store.MarkInstalled(profile.Kind, profile.ID, installed.Version, installed.Loader, installed.LoaderVersion); err != nil {
		return err
	}
	a.mu.Lock()
	op.Warnings = append([]string{}, plan.Warnings...)
	a.mu.Unlock()
	return nil
}
func contentPathAllowed(name, kind string) bool {
	if !download.SafeRelative(name) {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if strings.HasPrefix(strings.ToLower(component), ".neolauncher") {
			return false
		}
	}
	base := strings.ToLower(path.Base(name))
	return kind != "server" || (base != "eula.txt" && base != "server.properties")
}
func (a *App) publishContent(ctx context.Context, profile launcher.Target, plan *content.Plan, work string, report download.Reporter) error {
	type pending struct{ source, destination string }
	var publish []pending
	seen := map[string]bool{}
	root := filepath.Join(work, "files")
	for _, file := range plan.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !contentPathAllowed(file.Path, profile.Kind) {
			return errors.New("modpack contains a protected or unsafe path")
		}
		lower := strings.ToLower(file.Path)
		if seen[lower] {
			return errors.New("duplicate content destination")
		}
		seen[lower] = true
		dest, err := download.SafeJoin(profile.Directory, file.Path)
		if err != nil {
			return err
		}
		if info, err := os.Lstat(dest); err == nil {
			if !info.Mode().IsRegular() || download.Verify(dest, file) != nil {
				return fmt.Errorf("%s already exists with different content; nothing was overwritten", file.Path)
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		source, err := download.SafeJoin(root, file.Path)
		if err != nil {
			return err
		}
		file.AllowOverwrite = false
		file.AllowUnverified = false
		if err = a.downloads.Download(ctx, file, source, report); err != nil {
			return err
		}
		publish = append(publish, pending{source, dest})
	}
	for _, override := range plan.Overrides {
		if !contentPathAllowed(override.Path, profile.Kind) {
			return errors.New("modpack contains a protected or unsafe override")
		}
		lower := strings.ToLower(override.Path)
		if seen[lower] {
			return errors.New("duplicate content destination")
		}
		seen[lower] = true
		dest, err := download.SafeJoin(profile.Directory, override.Path)
		if err != nil {
			return err
		}
		if info, err := os.Lstat(dest); err == nil {
			if !info.Mode().IsRegular() {
				return errors.New("override destination is not a regular file")
			}
			if info.Size() != int64(len(override.Data)) {
				return fmt.Errorf("%s already exists; nothing was overwritten", override.Path)
			}
			existing, err := os.ReadFile(dest)
			if err != nil {
				return err
			}
			if !bytes.Equal(existing, override.Data) {
				return fmt.Errorf("%s already exists; nothing was overwritten", override.Path)
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		source, err := download.SafeJoin(root, override.Path)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(source), 0700); err != nil {
			return err
		}
		if err = os.WriteFile(source, override.Data, 0600); err != nil {
			return err
		}
		publish = append(publish, pending{source, dest})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var created []string
	committed := false
	defer func() {
		if !committed {
			for _, file := range created {
				_ = os.Remove(file)
			}
		}
	}()
	for i, file := range publish {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(file.destination), 0700); err != nil {
			return err
		}
		// An exclusive link cannot overwrite a file that appeared after validation.
		if err := os.Link(file.source, file.destination); err != nil {
			return fmt.Errorf("could not publish content without overwriting files: %w", err)
		}
		created = append(created, file.destination)
		if report != nil {
			report(download.Progress{Stage: "Installing content", Name: filepath.Base(file.destination), Done: i + 1, Total: len(publish)})
		}
	}
	committed = true
	if report != nil {
		report(download.Progress{Stage: "Content installed", Done: len(publish), Total: len(publish)})
	}
	return nil
}
