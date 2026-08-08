//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func acquireServerLock(lockFile string) (*os.File, error) {
	pathp, err := windows.UTF16PtrFromString(lockFile)
	if err != nil {
		return nil, fmt.Errorf("failed to convert lock file path to UTF16: %w", err)
	}

	// CreateFile(lpFileName, dwDesiredAccess, dwShareMode, lpSecurityAttributes, dwCreationDisposition, dwFlagsAndAttributes, hTemplateFile)
	// dwShareMode = 0 means exclusive lock: other processes cannot open it in any mode until closed.
	h, err := windows.CreateFile(
		pathp,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, // Exclusive access (no sharing)
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return nil, fmt.Errorf("elastic-mcp-server is already running (lock on %s)", lockFile)
		}
		return nil, fmt.Errorf("failed to create or open lock file %s: %w", lockFile, err)
	}

	return os.NewFile(uintptr(h), lockFile), nil
}
