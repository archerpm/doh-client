//go:build !linux

package main

func prepareWebKitSandbox() {}

func trayAvailable() (hasBus, hasWatcher bool) { return true, true }
