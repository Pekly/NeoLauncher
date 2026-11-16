package minecraft

import (
	"errors"
	"os"
	"strings"

	"github.com/Pekly/NeoLauncher/internal/download"
)

// ValidateInstallation permits retrying content downloads after the base game
// completed. It neither accepts the EULA nor runs Java or changes existing files.
func (s *Service) ValidateInstallation(directory string) (Installation, error) {
	i, err := s.ReadInstallation(directory)
	if err != nil {
		return Installation{}, err
	}
	rt, dir, root, err := s.loadRuntime(directory, i.Kind)
	if err != nil {
		return Installation{}, err
	}
	artifact, ok := rt.Metadata.Downloads[i.Kind]
	if !ok {
		return Installation{}, errors.New("installed game metadata is incomplete")
	}
	if i.Kind == "server" {
		jar, err := regularPath(dir, rt.ServerJar)
		if err != nil {
			return Installation{}, err
		}
		if err = download.Verify(jar, fileFor(artifact)); err != nil {
			return Installation{}, err
		}
	}
	if i.Kind == "client" || i.Loader == "fabric" {
		if _, err := classpath(root, rt); err != nil {
			return Installation{}, err
		}
		found := i.Kind != "client"
		for _, relative := range rt.Classpath {
			if strings.HasPrefix(relative, "cache/versions/") {
				p, err := regularPath(root, relative)
				if err == nil && download.Verify(p, fileFor(artifact)) == nil {
					found = true
				}
			}
		}
		if !found {
			return Installation{}, errors.New("installed game jar failed verification")
		}
	}
	if i.Kind == "client" {
		p, err := download.SafeJoin(dir, rt.NativeDir)
		if err != nil {
			return Installation{}, err
		}
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			return Installation{}, errors.New("native library directory is missing")
		}
	}
	return i, nil
}
