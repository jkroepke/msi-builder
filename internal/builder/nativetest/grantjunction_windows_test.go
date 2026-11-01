//go:build windows

package nativetest_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// grantJunctionConfig grants Guests full control on a folder whose parent
// the test replaces with a junction before the install (SEC-06).
const grantJunctionConfig = `schema: msi-builder/v1
product: { name: MsiBuilderGrantLink, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 2f0c6b8e-41d7-4a5e-9c3b-7e18d5a0f264 }
package: { platform: x64, compression: none }
installDir: { name: MsiBuilderGrantLink }
components:
  - id: Linked
    directory: CommonAppDataFolder/MsiBuilderGrantLink/link/child
    createFolder: true
    folderPermissions:
      grant: [{ account: S-1-5-32-546, rights: full }]
`

// TestMsiexecGrantThroughJunction checks SEC-06: when a folder above a
// granted folder is a junction to another tree, the deferred grant action
// refuses the path and fails the install, and the folder the junction leads
// to keeps its DACL. Before the grant went by a checked handle, it wrote
// the Guests ACE into the junction's target.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecGrantThroughJunction(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	root := filepath.Join(os.Getenv("ProgramData"), "MsiBuilderGrantLink")
	link := filepath.Join(root, "link")
	bait := filepath.Join(root, "bait")
	baitChild := filepath.Join(bait, "child")

	cleanup := func() {
		_ = os.Remove(link) // the junction only
		_ = os.RemoveAll(root)
	}
	cleanup()

	if err := os.MkdirAll(baitChild, 0o750); err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // test-controlled paths
	if out, err := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", link, bait).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v\n%s", err, out)
	}

	dir := t.TempDir()
	path := buildConfig(t, dir, "grantlink", grantJunctionConfig, nil)

	t.Cleanup(func() {
		_ = exec.CommandContext(context.Background(), "msiexec", "/x", path, "/qn", "/norestart").Run()

		cleanup()
	})

	before := objectSDDL(t, baitChild)
	logPath := filepath.Join(dir, "install.log")

	if code := msiexecCode(t, logPath, "/i", path); code != 1603 {
		t.Errorf("install through the junction returned %d, want 1603", code)
	}

	log := readLog(logPath)
	if !strings.Contains(log, "refused, reached through a junction") || !strings.Contains(log, baitChild) {
		t.Errorf("the log does not name the refused path and its target\n%s", grepLog(log, "BuilderGrant"))
	}

	after := objectSDDL(t, baitChild)
	t.Logf("SEC-06: bait\\child before %s, after %s", before, after)

	if !sameACEs(after, before) || strings.Contains(after, ";;;BG)") || strings.Contains(after, "S-1-5-32-546") {
		t.Errorf("the junction's target changed:\n%s\nwant\n%s", after, before)
	}
}
