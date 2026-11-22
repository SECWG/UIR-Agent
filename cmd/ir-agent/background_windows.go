package main

import (
	"os/exec"
	"syscall"
)

func detach(cmd *exec.Cmd) {
	// CREATE_NO_WINDOW works on legacy Windows too. Do not combine it with DETACHED_PROCESS.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
}
