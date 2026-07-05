//go:build linux
package main

import (
	"syscall"
)

func setParentDeathSignal() {
	_, _, _ = syscall.Syscall(syscall.SYS_PRCTL, syscall.PR_SET_PDEATHSIG, uintptr(syscall.SIGTERM), 0)
}
