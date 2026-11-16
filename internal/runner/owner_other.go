//go:build !windows

package runner

import (
	"os/exec"
	"syscall"
)

type unixOwner struct{ pid int }

func (o *unixOwner) Kill() error  { return syscall.Kill(-o.pid, syscall.SIGKILL) }
func (o *unixOwner) Close() error { return nil }
func startOwned(command *exec.Cmd, _ int) (owner, error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &unixOwner{pid: command.Process.Pid}, nil
}
