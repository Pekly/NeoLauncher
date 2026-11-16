package minecraft

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var javaVersionPattern = regexp.MustCompile(`(?m)(?:openjdk|java) version "([0-9]+)(?:\.([0-9]+))?[^"\r\n]*"`)

func parseJavaMajor(output string) (int, error) {
	m := javaVersionPattern.FindStringSubmatch(output)
	if len(m) == 0 {
		return 0, errors.New("Java returned an unrecognized version")
	}
	major, _ := strconv.Atoi(m[1])
	if major == 1 {
		major, _ = strconv.Atoi(m[2])
	}
	if major < 8 {
		return 0, errors.New("Java 8 or newer is required")
	}
	return major, nil
}

type boundedOutput struct {
	mu   sync.Mutex
	data []byte
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	left := 65536 - len(b.data)
	if len(p) > left {
		p = p[:left]
	}
	b.data = append(b.data, p...)
	return n, nil
}
func resolveJava(configured string, required int) (string, error) {
	name := configured
	if name == "" {
		if home := os.Getenv("JAVA_HOME"); home != "" {
			name = filepath.Join(home, "bin", "java")
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
		} else {
			name = "java"
		}
	}
	// javaw does not reliably expose -version output or server console streams.
	if runtime.GOOS == "windows" && strings.EqualFold(filepath.Base(name), "javaw.exe") {
		name = filepath.Join(filepath.Dir(name), "java.exe")
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("Java %d is required; configure an existing Java executable (or JAVA_HOME/PATH)", required)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-version")
	cmd.WaitDelay = time.Second
	hideJavaCheck(cmd)
	output := &boundedOutput{}
	cmd.Stdout = output
	cmd.Stderr = output
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", errors.New("Java version check timed out")
		}
		return "", errors.New("could not verify configured Java executable")
	}
	major, err := parseJavaMajor(string(output.data))
	if err != nil {
		return "", err
	}
	if major != required {
		return "", fmt.Errorf("this installation requires Java %d; configured Java is %d", required, major)
	}
	return path, nil
}
