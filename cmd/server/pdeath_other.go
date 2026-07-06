//go:build !linux

package main

func setParentDeathSignal() {
	// No-op on non-Linux platforms
}
