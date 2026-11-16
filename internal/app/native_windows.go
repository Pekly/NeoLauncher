//go:build windows

package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	winopts "github.com/wailsapp/wails/v2/pkg/options/windows"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

const Version = "0.2.0-dev"

func DefaultDataDir() (string, error) {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		var err error
		root, err = os.UserCacheDir()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(root, "NeoLauncher"), nil
}
func Run(dataDir string) error {
	root, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root))))
	class := fmt.Sprintf("NeoLauncher-%x", hash[:12])
	name, err := windows.UTF16PtrFromString("Local\\" + class)
	if err != nil {
		return err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if handle != 0 {
		defer windows.CloseHandle(handle)
	}
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		className, _ := windows.UTF16PtrFromString(class)
		user := windows.NewLazySystemDLL("user32.dll")
		window, _, _ := user.NewProc("FindWindowW").Call(uintptr(unsafe.Pointer(className)), 0)
		if window != 0 {
			user.NewProc("ShowWindow").Call(window, 9)
			user.NewProc("SetForegroundWindow").Call(window)
		}
		return nil
	}
	if err != nil {
		return err
	}
	app, err := New(root)
	if err != nil {
		return err
	}
	defer app.closeServices()
	return wails.Run(&options.App{
		Title: "NeoLauncher", Width: 1180, Height: 760, MinWidth: 840, MinHeight: 580, Frameless: true, BackgroundColour: options.NewRGB(16, 20, 27),
		AssetServer: &assetserver.Options{Assets: assets()}, OnStartup: func(ctx context.Context) { app.ctx = ctx }, OnBeforeClose: app.beforeClose, OnShutdown: func(context.Context) { _ = app.closeServices() }, Bind: []interface{}{app},
		Windows: &winopts.Options{Theme: winopts.Dark, WindowClassName: class, WebviewUserDataPath: filepath.Join(root, "webview"), DLLSearchPaths: winopts.DLLSearchSystem32},
	})
}
func (a *App) beforeClose(ctx context.Context) bool {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return false
	}
	if a.closing || a.prompt {
		a.mu.Unlock()
		return true
	}
	a.prompt = true
	downloads := false
	for _, op := range a.operations {
		if op.State == "running" {
			downloads = true
		}
	}
	a.mu.Unlock()
	active := a.runner.ActiveCount()
	if active > 0 || downloads {
		message := "Cancel downloads and quit?\n\nNo: minimize and keep working."
		if active > 0 {
			message = "Quit NeoLauncher?\n\nRunning Minecraft games will close and may lose unsaved single-player progress. Servers will be asked to save and stop. Downloads will be cancelled.\n\nNo: minimize and keep running."
		}
		choice, err := wruntime.MessageDialog(ctx, wruntime.MessageDialogOptions{Type: wruntime.QuestionDialog, Title: "Quit NeoLauncher?", Message: message, DefaultButton: "No"})
		if err != nil || choice != "Yes" {
			a.mu.Lock()
			a.prompt = false
			a.mu.Unlock()
			if err == nil {
				wruntime.WindowMinimise(ctx)
			}
			return true
		}
	}
	a.mu.Lock()
	a.prompt = false
	a.closing = true
	a.mu.Unlock()
	wruntime.WindowSetTitle(ctx, "NeoLauncher — Stopping…")
	go func() { _ = a.closeServices(); a.mu.Lock(); a.closed = true; a.mu.Unlock(); wruntime.Quit(ctx) }()
	return true
}
func (a *App) ChooseJava() (AppState, error) {
	if a.ctx == nil {
		return AppState{}, errors.New("window is not ready")
	}
	file, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{Title: "Choose Java", Filters: []wruntime.FileFilter{{DisplayName: "Java executable", Pattern: "java.exe"}}})
	if err != nil {
		return AppState{}, err
	}
	if file == "" {
		return a.GetState(), nil
	}
	settings := a.GetState().Settings
	settings.JavaPath = file
	return a.SaveSettings(settings)
}
func (a *App) OpenDataFolder() error { return openFolder(a.GetState().DataDir) }
func (a *App) OpenServerFolder(id string) error {
	directory, err := a.store.ServerDirectory(id)
	if err != nil {
		return err
	}
	return openFolder(directory)
}
func (a *App) OpenInstanceFolder(id string) error {
	directory, err := a.store.InstanceDirectory(id)
	if err != nil {
		return err
	}
	return openFolder(directory)
}
func (a *App) OpenExternal(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return errors.New("external link is not allowed")
	}
	allowed := false
	for _, host := range []string{"microsoft.com", "www.microsoft.com", "login.microsoftonline.com", "login.live.com", "www.minecraft.net", "minecraft.net", "aka.ms", "modrinth.com", "www.curseforge.com", "curseforge.com", "github.com"} {
		if u.Hostname() == host {
			allowed = true
		}
	}
	if !allowed || a.ctx == nil {
		return errors.New("external link is not allowed")
	}
	wruntime.BrowserOpenURL(a.ctx, u.String())
	return nil
}
func openFolder(directory string) error {
	info, err := os.Stat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("folder is unavailable")
	}
	command := exec.Command("explorer.exe", directory)
	if err = command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
func ShowError(err error) {
	text, _ := windows.UTF16PtrFromString(err.Error())
	title, _ := windows.UTF16PtrFromString("NeoLauncher")
	windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
}
