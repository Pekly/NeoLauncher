package minecraft

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Pekly/NeoLauncher/internal/download"
)

const maxNativeBytes uint64 = 256 << 20
const maxNativeFileBytes uint64 = 64 << 20

func extractNatives(archive, directory string, excludes []string) error {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer z.Close()
	if len(z.File) > 4096 {
		return errors.New("native archive contains too many entries")
	}
	var total uint64
	for _, f := range z.File {
		name := strings.TrimSuffix(f.Name, "/")
		if !download.SafeRelative(name) || f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) {
			return errors.New("unsafe native archive entry")
		}
		if f.UncompressedSize64 > maxNativeFileBytes || f.UncompressedSize64 > maxNativeBytes-total {
			return errors.New("native archive exceeds extraction limit")
		}
		total += f.UncompressedSize64
		skip := strings.HasPrefix(name, "META-INF/") || name == "META-INF"
		for _, exclude := range excludes {
			if strings.HasPrefix(f.Name, exclude) {
				skip = true
			}
		}
		if skip || f.FileInfo().IsDir() {
			continue
		}
		dest, e := download.SafeJoin(directory, name)
		if e != nil {
			return e
		}
		r, e := f.Open()
		if e != nil {
			return e
		}
		b, e := io.ReadAll(io.LimitReader(r, int64(maxNativeFileBytes)+1))
		closeErr := r.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if uint64(len(b)) != f.UncompressedSize64 || uint64(len(b)) > maxNativeFileBytes {
			return errors.New("native archive size mismatch")
		}
		if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
			return e
		}
		// Shared duplicate license/native files are acceptable only when identical.
		if old, e := os.ReadFile(dest); e == nil {
			if !bytes.Equal(old, b) {
				return errors.New("conflicting native archive entries")
			}
			continue
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		out, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = out.Write(b)
		closeErr = out.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
