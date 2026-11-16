package app

import (
	"embed"
	"io/fs"
)

//go:embed frontend
var bundled embed.FS

func assets() fs.FS {
	result, err := fs.Sub(bundled, "frontend")
	if err != nil {
		panic(err)
	}
	return result
}
