//go:build !windows && !linux && !darwin

package main

func registerWindowsAppID() error   { return nil }
func unregisterWindowsAppID() error { return nil }

func registerURLScheme(exePath string) error { return nil }
func unregisterURLScheme() error             { return nil }
