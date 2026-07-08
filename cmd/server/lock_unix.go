//go:build !windows

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func acquireServerLock(lockFile string) (*os.File, error) {
	lf, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock file %s: %w", lockFile, err)
	}

	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lf.Close()
		pidData, _ := os.ReadFile(lockFile)
		pidStr := strings.TrimSpace(string(pidData))
		if pidStr != "" {
			if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
				if proc, err := os.FindProcess(pid); err == nil {
					if err := proc.Signal(syscall.Signal(0)); err == nil {
						return nil, fmt.Errorf("elastic-mcp-server (PID %d) is already running", pid)
					}
				}
			}
		}
		return nil, fmt.Errorf("elastic-mcp-server is already running (lock on %s)", lockFile)
	}

	return lf, nil
}
