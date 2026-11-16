//go:build windows

package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/Pekly/NeoLauncher/internal/launcher"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	winopts "github.com/wailsapp/wails/v2/pkg/options/windows"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

const Version = "0.1.0-dev"

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
		return errors.New("NeoLauncher is already open for this data folder")
	}
	if err != nil {
		return err
	}
	app, err := New(root)
	if err != nil {
		return err
	}
	return wails.Run(&options.App{
		Title: "NeoLauncher", Width: 1180, Height: 760, MinWidth: 840, MinHeight: 580, Frameless: true,
		BackgroundColour: options.NewRGB(16, 20, 27),
		AssetServer:      &assetserver.Options{Assets: assets()},
		OnStartup:        func(ctx context.Context) { app.ctx = ctx },
		Bind:             []interface{}{app},
		Windows:          &winopts.Options{Theme: winopts.Dark, WindowClassName: class, WebviewUserDataPath: filepath.Join(root, "webview"), DLLSearchPaths: winopts.DLLSearchSystem32},
	})
}
func (a *App) ChooseJava() (launcher.State, error) {
	if a.ctx == nil {
		return launcher.State{}, errors.New("window is not ready")
	}
	file, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{Title: "Choose Java", Filters: []wruntime.FileFilter{{DisplayName: "Java executable", Pattern: "java.exe"}}})
	if err != nil {
		return launcher.State{}, err
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
