//go:build !unix

package proc

import (
	"errors"
	"os"
	"os/exec"
)

func Group(cmd *exec.Cmd)  {}
func Detach(cmd *exec.Cmd) {}

func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.FindProcess(pid)
	return err == nil
}

func Terminate(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := p.Kill(); err != nil {
		return errors.Join(errors.New("could not stop process"), err)
	}
	return nil
}
