//go:build !windows

package main

// registerWindowsAppID and unregisterWindowsAppID are Windows-only. The
// runtime.GOOS branches in cmd_daemon_install.go never invoke them on
// other platforms, but the symbols must resolve at build time.

func registerWindowsAppID() error   { return nil }
func unregisterWindowsAppID() error { return nil }
