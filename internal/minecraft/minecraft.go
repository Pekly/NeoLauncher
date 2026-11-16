// Package minecraft installs vendor-verified game files and prepares private launch
// specifications. It never starts a game, accepts the EULA, or installs Java.
package minecraft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/Pekly/NeoLauncher/internal/download"
)

type Version struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}
type Options struct {
	Version       string `json:"version"`
	Loader        string `json:"loader"`
	LoaderVersion string `json:"loader_version"`
}
type Installation struct {
	Version       string `json:"version"`
	Loader        string `json:"loader"`
	LoaderVersion string `json:"loader_version"`
	JavaMajor     int    `json:"java_major"`
	Kind          string `json:"kind"`
}
type Identity struct {
	Name        string `json:"-"`
	UUID        string `json:"-"`
	AccessToken string `json:"-"`
	XUID        string `json:"-"`
}

// LaunchSpec is process-only data. Even accidental JSON serialization cannot expose credentials.
type LaunchSpec struct {
	Program   string   `json:"-"`
	Args      []string `json:"-"`
	Directory string   `json:"-"`
	Secrets   []string `json:"-"`
}
type platform struct{ OS, Arch, Version string }
type Service struct {
	root                             string
	client                           *download.Client
	manifestURL, fabricURL, assetURL string
	platform                         platform
	javaResolver                     func(string, int) (string, error)
	cacheLocks                       [128]sync.Mutex
}

func New(root string, client *download.Client) *Service {
	if client == nil {
		client = download.New()
	}
	osName := runtime.GOOS
	if osName == "darwin" {
		osName = "osx"
	}
	return &Service{root: root, client: client, manifestURL: "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json", fabricURL: "https://meta.fabricmc.net/v2", assetURL: "https://resources.download.minecraft.net", platform: platform{OS: osName, Arch: runtime.GOARCH, Version: osVersion()}, javaResolver: resolveJava}
}

const installationFile = ".neolauncher-install.json"
const runtimeFile = ".neolauncher-runtime.json"

type artifact struct {
	Path string `json:"path"`
	URL  string `json:"url"`
	SHA1 string `json:"sha1"`
	Size int64  `json:"size"`
}
type rule struct {
	Action   string            `json:"action"`
	OS       map[string]string `json:"os,omitempty"`
	Features map[string]bool   `json:"features,omitempty"`
}
type library struct {
	Name      string `json:"name"`
	URL       string `json:"url,omitempty"`
	Downloads struct {
		Artifact    *artifact           `json:"artifact"`
		Classifiers map[string]artifact `json:"classifiers"`
	} `json:"downloads"`
	Rules   []rule            `json:"rules,omitempty"`
	Natives map[string]string `json:"natives,omitempty"`
	Extract struct {
		Exclude []string `json:"exclude"`
	} `json:"extract"`
}
type metadata struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	MainClass    string `json:"mainClass"`
	InheritsFrom string `json:"inheritsFrom,omitempty"`
	JavaVersion  struct {
		MajorVersion int `json:"majorVersion"`
	} `json:"javaVersion"`
	Downloads  map[string]artifact `json:"downloads"`
	Libraries  []library           `json:"libraries"`
	AssetIndex struct {
		ID   string `json:"id"`
		URL  string `json:"url"`
		SHA1 string `json:"sha1"`
		Size int64  `json:"size"`
	} `json:"assetIndex"`
	Assets    string `json:"assets"`
	Arguments struct {
		Game []json.RawMessage `json:"game"`
		JVM  []json.RawMessage `json:"jvm"`
	} `json:"arguments"`
	MinecraftArguments string `json:"minecraftArguments,omitempty"`
	Logging            map[string]struct {
		Argument string   `json:"argument"`
		File     artifact `json:"file"`
	} `json:"logging,omitempty"`
}
type installedRuntime struct {
	Installation Installation `json:"installation"`
	Platform     platform     `json:"platform"`
	Metadata     metadata     `json:"metadata"`
	Classpath    []string     `json:"classpath"`    // relative to Service.root
	NativeDir    string       `json:"native_dir"`   // relative to profile
	ServerJar    string       `json:"server_jar"`   // relative to profile
	LoggingFile  string       `json:"logging_file"` // relative to Service.root
}
type manifest struct {
	Versions []struct {
		Version
		URL  string `json:"url"`
		SHA1 string `json:"sha1"`
	} `json:"versions"`
}

func (s *Service) Versions(ctx context.Context) ([]Version, error) {
	var m manifest
	if err := s.client.JSON(ctx, s.manifestURL, &m, nil); err != nil {
		return nil, err
	}
	out := make([]Version, 0, len(m.Versions))
	for _, v := range m.Versions {
		if !safeID(v.ID) {
			return nil, errors.New("invalid version ID")
		}
		out = append(out, v.Version)
	}
	return out, nil
}
func safeID(s string) bool {
	// Official historical IDs include spaces (for example 1.14 Pre-Release 1).
	return download.SafeRelative(s) && !strings.Contains(s, "/") && !strings.ContainsAny(s, "\t?#%")
}
func normalized(o Options) (Options, error) {
	if o.Loader == "" {
		o.Loader = "vanilla"
	}
	if !safeID(o.Version) {
		return o, errors.New("select a valid Minecraft version")
	}
	if o.Loader != "vanilla" && o.Loader != "fabric" {
		return o, errors.New("unsupported loader: only vanilla and fabric are supported")
	}
	if o.Loader == "vanilla" && o.LoaderVersion != "" {
		return o, errors.New("vanilla has no loader version")
	}
	if o.LoaderVersion != "" && !safeID(o.LoaderVersion) {
		return o, errors.New("invalid loader version")
	}
	return o, nil
}
func readJSON(path string, dest any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return errors.New("invalid metadata file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 32<<20))
	if err = dec.Decode(dest); err != nil {
		return errors.New("invalid installed metadata")
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return errors.New("trailing installed metadata")
	}
	return nil
}
func writeJSON(root, name string, value any) error {
	dest, err := download.SafeJoin(root, name)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		return errors.New("installation metadata already exists")
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	closeErr := f.Close()
	if err != nil {
		os.Remove(dest)
		return err
	}
	if closeErr != nil {
		os.Remove(dest)
		return closeErr
	}
	return nil
}

// profileRoot rejects symlinks in all existing ancestors, not only the final directory.
func profileRoot(directory string) (string, error) {
	abs, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	for p := abs; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return "", errors.New("profile/cache path must contain only real directories")
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return "", e
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return abs, nil
}
func prepareDirectory(directory string) (string, error) {
	dir, err := profileRoot(directory)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		// These are caller-owned setup files: never modify either one.
		if entry.Name() != "server.properties" && entry.Name() != "eula.txt" {
			return "", fmt.Errorf("profile must be empty or contain only server.properties/eula.txt (found %s)", entry.Name())
		}
		info, e := entry.Info()
		if e != nil || !info.Mode().IsRegular() {
			return "", errors.New("unsafe setup file")
		}
	}
	return dir, nil
}
func (s *Service) ReadInstallation(directory string) (Installation, error) {
	var i Installation
	dir, err := profileRoot(directory)
	if err != nil {
		return i, err
	}
	p, err := download.SafeJoin(dir, installationFile)
	if err != nil {
		return i, err
	}
	if err = readJSON(p, &i); err != nil {
		return i, err
	}
	o, err := normalized(Options{i.Version, i.Loader, i.LoaderVersion})
	if err != nil || o.Loader != i.Loader || i.JavaMajor < 8 || i.JavaMajor > 99 || (i.Kind != "client" && i.Kind != "server") || (i.Loader == "fabric" && i.LoaderVersion == "") {
		return Installation{}, errors.New("invalid installation record")
	}
	return i, nil
}
func emit(report download.Reporter, stage, name string, done, total int) {
	if report != nil {
		report(download.Progress{Stage: stage, Name: name, Done: done, Total: total})
	}
}
