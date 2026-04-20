//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func registerWindowsAppID() error   { return nil }
func unregisterWindowsAppID() error { return nil }

func registerURLScheme(exePath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	appDir := filepath.Join(home, "Applications", "ClawdChan.app")
	contentsDir := filepath.Join(appDir, "Contents")
	macOSDir := filepath.Join(contentsDir, "MacOS")
	
	if err := os.MkdirAll(macOSDir, 0o755); err != nil {
		return fmt.Errorf("mkdir bundle: %w", err)
	}

	plistContent := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key>
	<string>ClawdChan-Handler</string>
	<key>CFBundleIdentifier</key>
	<string>com.vmaroon.clawdchan.handler</string>
	<key>CFBundleName</key>
	<string>ClawdChan URL Handler</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleURLTypes</key>
	<array>
		<dict>
			<key>CFBundleURLName</key>
			<string>ClawdChan Protocol</string>
			<key>CFBundleURLSchemes</key>
			<array>
				<string>clawdchan</string>
			</array>
		</dict>
	</array>
</dict>
</plist>
`
	if err := os.WriteFile(filepath.Join(contentsDir, "Info.plist"), []byte(plistContent), 0o644); err != nil {
		return fmt.Errorf("write Info.plist: %w", err)
	}

	handlerContent := fmt.Sprintf(`#!/bin/sh
exec "%s" consume --web "$1"
`, exePath)
	handlerPath := filepath.Join(macOSDir, "ClawdChan-Handler")
	if err := os.WriteFile(handlerPath, []byte(handlerContent), 0o755); err != nil {
		return fmt.Errorf("write handler: %w", err)
	}

	lsregister := "/System/Library/Frameworks/CoreServices.framework/Versions/A/Frameworks/LaunchServices.framework/Versions/A/Support/lsregister"
	if out, err := exec.Command(lsregister, "-f", appDir).CombinedOutput(); err != nil {
		return fmt.Errorf("lsregister: %w: %s", err, string(out))
	}
	return nil
}

func unregisterURLScheme() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	appDir := filepath.Join(home, "Applications", "ClawdChan.app")
	if _, err := os.Stat(appDir); os.IsNotExist(err) {
		return nil
	}
	
	lsregister := "/System/Library/Frameworks/CoreServices.framework/Versions/A/Frameworks/LaunchServices.framework/Versions/A/Support/lsregister"
	_ = exec.Command(lsregister, "-u", appDir).Run()
	
	return os.RemoveAll(appDir)
}
