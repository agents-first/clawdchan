//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/agents-first/clawdchan/core/notify"
)

// registerWindowsAppID writes the HKCU\Software\Classes\AppUserModelId entry
// that WinRT requires before it will accept toasts bound to a custom AppID.
// Without this, CreateToastNotifier("ClawdChan") throws and no toast fires.
//
// This is per-user state and does not require admin. Windows desktop toasts
// need both pieces on clean machines:
//   - the AppUserModelId registry key so the app appears in notification
//     settings with the right display name/icon;
//   - a Start Menu shortcut tagged with System.AppUserModel.ID, which is the
//     documented identity check for unpackaged desktop apps.
func registerWindowsAppID() error {
	key := `HKCU\Software\Classes\AppUserModelId\` + notify.WindowsAppID
	if out, err := exec.Command("reg", "add", key,
		"/v", "DisplayName", "/t", "REG_SZ", "/d", "ClawdChan", "/f").CombinedOutput(); err != nil {
		return fmt.Errorf("reg add DisplayName: %w: %s", err, string(out))
	}
	if out, err := exec.Command("reg", "add", key,
		"/v", "ShowInSettings", "/t", "REG_DWORD", "/d", "1", "/f").CombinedOutput(); err != nil {
		return fmt.Errorf("reg add ShowInSettings: %w: %s", err, string(out))
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable for toast shortcut: %w", err)
	}
	if out, err := exec.Command("reg", "add", key,
		"/v", "IconUri", "/t", "REG_SZ", "/d", exe, "/f").CombinedOutput(); err != nil {
		return fmt.Errorf("reg add IconUri: %w: %s", err, string(out))
	}
	return installWindowsToastShortcut(exe)
}

// unregisterWindowsAppID removes the HKCU AUMID entry. Best-effort — errors
// are ignored because uninstall shouldn't fail on cleanup of a stale entry.
func unregisterWindowsAppID() error {
	key := `HKCU\Software\Classes\AppUserModelId\` + notify.WindowsAppID
	_ = exec.Command("reg", "delete", key, "/f").Run()
	return uninstallWindowsToastShortcut()
}

func installWindowsToastShortcut(exe string) error {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return fmt.Errorf("APPDATA not set; cannot create Start Menu toast shortcut")
	}
	shortcut := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "ClawdChan.lnk")
	if err := os.MkdirAll(filepath.Dir(shortcut), 0o755); err != nil {
		return fmt.Errorf("create Start Menu programs dir: %w", err)
	}

	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$shortcutPath = %s
$targetPath = %s
$appID = %s
$shell = New-Object -ComObject WScript.Shell
$shortcut = $shell.CreateShortcut($shortcutPath)
$shortcut.TargetPath = $targetPath
$shortcut.Arguments = ""
$shortcut.Description = "ClawdChan"
$shortcut.WorkingDirectory = [System.IO.Path]::GetDirectoryName($targetPath)
$shortcut.IconLocation = "$targetPath,0"
$shortcut.Save()
[void][System.Runtime.InteropServices.Marshal]::FinalReleaseComObject($shortcut)
[void][System.Runtime.InteropServices.Marshal]::FinalReleaseComObject($shell)

Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;

[ComImport, Guid("00021401-0000-0000-C000-000000000046")]
class ShellLink {}

[ComImport, InterfaceType(ComInterfaceType.InterfaceIsIUnknown), Guid("0000010b-0000-0000-C000-000000000046")]
interface IPersistFile {
    void GetClassID(out Guid pClassID);
    void IsDirty();
    void Load([MarshalAs(UnmanagedType.LPWStr)] string pszFileName, uint dwMode);
    void Save([MarshalAs(UnmanagedType.LPWStr)] string pszFileName, bool fRemember);
    void SaveCompleted([MarshalAs(UnmanagedType.LPWStr)] string pszFileName);
    void GetCurFile([MarshalAs(UnmanagedType.LPWStr)] out string ppszFileName);
}

[ComImport, InterfaceType(ComInterfaceType.InterfaceIsIUnknown), Guid("886D8EEB-8CF2-4446-8D02-CDBA1DBDCF99")]
interface IPropertyStore {
    void GetCount(out uint cProps);
    void GetAt(uint iProp, out PropertyKey pkey);
    void GetValue(ref PropertyKey key, out PropVariant pv);
    void SetValue(ref PropertyKey key, ref PropVariant pv);
    void Commit();
}

[StructLayout(LayoutKind.Sequential, Pack = 4)]
struct PropertyKey {
    public Guid fmtid;
    public uint pid;
    public PropertyKey(Guid fmtid, uint pid) { this.fmtid = fmtid; this.pid = pid; }
}

[StructLayout(LayoutKind.Sequential)]
struct PropVariant : IDisposable {
    ushort vt;
    ushort wReserved1;
    ushort wReserved2;
    ushort wReserved3;
    IntPtr p;
    int p2;

    public static PropVariant FromString(string value) {
        PropVariant pv = new PropVariant();
        pv.vt = 31; // VT_LPWSTR
        pv.p = Marshal.StringToCoTaskMemUni(value);
        return pv;
    }

    public void Dispose() { PropVariantClear(ref this); }

    [DllImport("ole32.dll")]
    static extern int PropVariantClear(ref PropVariant pvar);
}

public static class ClawdChanToastShortcut {
    public static void SetAppID(string shortcutPath, string appID) {
        object link = new ShellLink();
        IPersistFile file = (IPersistFile)link;
        file.Load(shortcutPath, 2); // STGM_READWRITE

        IPropertyStore propStore = (IPropertyStore)link;
        PropertyKey appIDKey = new PropertyKey(new Guid("9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3"), 5);
        PropVariant pv = PropVariant.FromString(appID);
        try {
            propStore.SetValue(ref appIDKey, ref pv);
            propStore.Commit();
        } finally {
            pv.Dispose();
        }

        file.Save(shortcutPath, true);
    }
}
'@
[ClawdChanToastShortcut]::SetAppID($shortcutPath, $appID)
`, psQuote(shortcut), psQuote(exe), psQuote(notify.WindowsAppID))

	out, err := exec.Command("powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create Start Menu toast shortcut: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func uninstallWindowsToastShortcut() error {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return nil
	}
	shortcut := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "ClawdChan.lnk")
	if err := os.Remove(shortcut); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
