//go:build !windows

package minecraft

import "os/exec"

// Version-specific rules fail closed on other OSes instead of guessing a kernel version.
func osVersion() string           { return "" }
func hideJavaCheck(cmd *exec.Cmd) {}
