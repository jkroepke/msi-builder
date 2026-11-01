//go:build windows

package nativetest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	removeUIName  = "MsiBuilderRemoveUI"
	removeUILabel = "Delete the configuration"
	// removeUIAdvance presses through the maintenance welcome page, Remove
	// in the menu and on the confirmation page, the remove dialog's Next,
	// Finish, and Yes on the box msiexec /x asks first. Install answers
	// ResumeDlg, so a /x that shows it ends instead of timing out.
	removeUIAdvance = "^(Next|Remove|Finish|Yes|Install)$"
)

const removeUIConfig = `schema: msi-builder/v1

product:
  name: MsiBuilderRemoveUI
  manufacturer: msi-builder tests
  version: {{ .VERSION | quote }}
  language: 1033
  upgradeCode: 2d7c1b94-6e3a-4f05-8b21-9a4c3e7f0d58

package:
  platform: x64
  scope: perMachine
  installerVersion: "5.0"

components:
  - files:
      - source: {{ .SOURCE | quote }}
        name: tool.exe
  - id: Config
    directory: CommonAppDataFolder/MsiBuilderRemoveUI
    createFolder: true
    purge:
      condition: DELETE_CONFIGURATION="1"

properties:
  DELETE_CONFIGURATION: {}

ui:
  enabled: true
  dialogs:
    - id: RemoveOptionsDlg
      flow: remove
      title: Remove options
      description: What to remove with [ProductName].
      fields:
        - property: DELETE_CONFIGURATION
          type: checkbox
          label: '&Delete the configuration'
`

// TestFullUIRemoveDialog removes the product three times through the full
// UI, reinstalling it quietly in between: the ARP entry's ModifyPath command
// with the remove dialog's box left clear keeps the purged folder, msiexec
// /x shows neither the maintenance menu nor the page and keeps it too, and
// the menu with the box checked deletes it, because the secure property
// reaches the deferred purge on the server (UI-09). It also records what Add or Remove Programs is given
// for a package whose ARPNOMODIFY stays unset (UI-10).
func TestFullUIRemoveDialog(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	requireFullUI(t)
	waitForMsiexec(t)

	dir := t.TempDir()
	pkg, res := buildFullUIFrom(t, dir, removeUIConfig, "1.0.0", "auto")
	data := filepath.Join(os.Getenv("ProgramData"), removeUIName)

	cleanupFullUI(t, pkg)
	t.Cleanup(func() {
		_ = os.RemoveAll(data)
		_ = os.RemoveAll(filepath.Join(os.Getenv("ProgramFiles"), removeUIName))
	})

	install := func(name string) {
		t.Helper()

		log := filepath.Join(dir, name+".log")
		if code := msiexec(t, "/i", pkg, "/qn", "/l*v", log); code != 0 {
			t.Fatalf("installing: exit %d\n%s", code, readLog(log))
		}

		if err := os.WriteFile(filepath.Join(data, "config.yaml"), []byte("listen: 9182\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	install("install1")

	arp := reg(t, "query", `HKLM\Software\Microsoft\Windows\CurrentVersion\Uninstall\`+res.ProductCode, "/reg:64")
	if !strings.Contains(arp, removeUIName) {
		t.Fatalf("the ARP entry was not read:\n%s", arp)
	}

	if strings.Contains(arp, "NoModify") {
		t.Errorf("the ARP entry has NoModify:\n%s", arp)
	}

	t.Logf("UI-10 ARP entry:\n%s", arp)

	// The ModifyPath command, which Change in Add or Remove Programs is
	// expected to run, the box left clear.
	modify := strings.Fields(arpValue(arp, "ModifyPath"))
	if len(modify) != 2 || !strings.EqualFold(modify[0], "MsiExec.exe") || !strings.EqualFold(modify[1], "/I"+res.ProductCode) {
		t.Errorf("ModifyPath %q is not MsiExec.exe /I%s; running the package instead", modify, res.ProductCode)

		modify = []string{"msiexec", "/i", pkg}
	}

	keepLog := filepath.Join(dir, "keep.log")
	code, keep := fullUIDriven(t, dir, "", removeUIAdvance, append(modify[1:], "/l*v", keepLog)...)

	if code != 0 {
		t.Errorf("remove keeping the configuration: exit %d, want 0", code)
	}

	if !strings.Contains(keep, "Remove options") || !strings.Contains(keep, removeUILabel) {
		t.Error("the maintenance remove did not show RemoveOptionsDlg")
	}

	if productInstalled(t, res.ProductCode) {
		t.Error("the product is still installed after the remove keeping the configuration")
	}

	if got := serverProperties(readLog(keepLog))["DELETE_CONFIGURATION"]; got != "" {
		t.Errorf("DELETE_CONFIGURATION on the server = %q with the box clear, want empty", got)
	}

	if _, err := os.Stat(filepath.Join(data, "config.yaml")); err != nil {
		t.Errorf("the configuration is gone with the box clear: %v", err)
	}

	// msiexec /x: no wizard page.
	install("install2")

	xLog := filepath.Join(dir, "x.log")
	code, removeX := fullUIDriven(t, dir, "", removeUIAdvance, "/x", pkg, "/l*v", xLog)

	if code != 0 {
		t.Errorf("msiexec /x: exit %d, want 0", code)
	}

	if strings.Contains(removeX, "Remove options") || strings.Contains(removeX, "Change, repair, or remove installation") {
		t.Error("msiexec /x showed the maintenance menu or the remove page")
	}

	if productInstalled(t, res.ProductCode) {
		t.Error("the product is still installed after msiexec /x")
	}

	if _, err := os.Stat(filepath.Join(data, "config.yaml")); err != nil {
		t.Errorf("the configuration is gone after msiexec /x: %v", err)
	}

	// The maintenance menu, the box checked.
	install("install3")

	deleteLog := filepath.Join(dir, "delete.log")
	code, remove := fullUIDriven(t, dir, "^"+removeUILabel+"$", removeUIAdvance, "/i", pkg, "/l*v", deleteLog)
	text := readLog(deleteLog)

	if code != 0 {
		t.Errorf("remove deleting the configuration: exit %d, want 0", code)
	}

	if !strings.Contains(remove, "Remove options") || !strings.Contains(remove, "CHOSE: ") {
		t.Error("the driver found no RemoveOptionsDlg with its Delete the configuration box")
	}

	if got := serverProperties(text)["DELETE_CONFIGURATION"]; got != "1" {
		t.Errorf("DELETE_CONFIGURATION on the server = %q, want 1", got)
	}

	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Errorf("the configuration folder is still there after the remove with the box checked: %v", err)
	}

	if strings.Contains(keep+removeX+remove, "UNEXPECTED:") {
		t.Error("the engine showed an unexpected error box")
	}

	t.Logf("UI-09 keep:\n%s\ndelete:\n%s\nlog excerpt:\n%s", keep, remove, excerpt(text, "DELETE_CONFIGURATION", "Purge_", "Error 28"))
	t.Logf("UI-10 msiexec /x:\n%s\nlog excerpt:\n%s", removeX, excerpt(readLog(xLog), "UILevel", "Dialog created", "Note: 1: 2205"))
}

// arpValue returns the data of a REG_SZ or REG_EXPAND_SZ value in reg.exe
// query output, or "".
func arpValue(out, name string) string {
	for line := range strings.Lines(out) {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == name && strings.HasPrefix(fields[1], "REG_") {
			return strings.Join(fields[2:], " ")
		}
	}

	return ""
}

// TestICEFullUIRemove runs the ICE suite on the package with a remove
// dialog.
//
//nolint:paralleltest // msi.dll's external UI handler is process-wide
func TestICEFullUIRemove(t *testing.T) {
	cub := iceCube(t)
	pkg, _ := buildFullUIFrom(t, t.TempDir(), removeUIConfig, "1.0.0", "auto")
	requireCleanICE(t, pkg, cub)
}
