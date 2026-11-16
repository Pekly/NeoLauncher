//go:build windows

package main

import (
	"flag"
	"fmt"
	"github.com/Pekly/NeoLauncher/internal/app"
	"os"
)

func main() {
	dataDir := flag.String("data-dir", "", "Use a separate data folder")
	version := flag.Bool("version", false, "Print the version")
	flag.Parse()
	if *version {
		fmt.Println("NeoLauncher " + app.Version)
		return
	}
	if *dataDir == "" {
		var err error
		*dataDir, err = app.DefaultDataDir()
		if err != nil {
			app.ShowError(err)
			os.Exit(1)
		}
	}
	if err := app.Run(*dataDir); err != nil {
		app.ShowError(err)
		os.Exit(1)
	}
}
