package minecraft

import (
	"crypto/rand"
	"errors"
	"os"
)

// AcceptEULA records explicit user consent without truncating a linked file.
func (s *Service) AcceptEULA(directory string) error {
	dir, err := profileRoot(directory)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if info, err := root.Lstat("eula.txt"); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("EULA must be a regular file, not a link")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	name := ".eula-" + rand.Text()
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	_, writeErr := f.WriteString("# Accepted explicitly in NeoLauncher. https://aka.ms/MinecraftEULA\neula=true\n")
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	// Rename replaces the directory entry, never follows the old file. Root
	// confinement also prevents a concurrent link swap from escaping the profile.
	return root.Rename(name, "eula.txt")
}
