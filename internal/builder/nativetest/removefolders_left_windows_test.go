//go:build windows

package nativetest_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/testpe"
)

// removeFoldersLeftConfig removes DATA_DIR on uninstall (FILE-14). With
// RECREATE=1, Recreate makes a new folder at DATA_DIR after the deferred
// action moved the old one aside and fails the removal, so the rollback
// cannot move the folder back.
const removeFoldersLeftConfig = `schema: msi-builder/v1
product: { name: MsiBuilderRemoveLeft, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 61c9e0a4-3d2b-4f87-b5e1-9a7c0d4f2e38 }
package: { platform: x64, compression: none }
installDir: { name: MsiBuilderRemoveLeft }
properties:
  DATA_DIR: {}
  RECREATE: { secure: true }
components:
  - files: [{ source: app.exe }]
    removeFolders:
      - property: DATA_DIR
customActions:
  - id: Recreate
    run: { command: '"[System64Folder]cmd.exe" /c mkdir "[DATA_DIR]new" & exit 1' }
    execute: deferred
    impersonate: false
    return: check
    schedule: [{ sequence: execute, before: InstallFinalize, condition: REMOVE~="ALL" AND RECREATE="1" }]
`

// leftLine matches the log line of a folder that cannot be restored and
// captures the backup folder and its token.
var leftLine = regexp.MustCompile(`cannot be restored, it stays in ([A-Za-z]:\\BuilderRemoveFolders\.([0-9a-f]{32})\\1)`)

// TestMsiexecRemoveFoldersKeepsLeft checks FILE-14: when the rollback of a
// removal cannot move a folder back, the log names the backup folder it
// stays in, and the mapping from the backup folder's random name to the
// original path is kept in the registry instead of going with the record.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecRemoveFoldersKeepsLeft(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	root := filepath.Join(os.Getenv("ProgramData"), "MsiBuilderRemoveLeft")
	data := filepath.Join(root, "data")
	dir := t.TempDir()
	path := buildConfig(t, dir, "removeleft", removeFoldersLeftConfig, map[string][]byte{
		"app.exe": testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}}),
	})

	var backup, token string

	t.Cleanup(func() {
		ctx := context.Background()
		_ = exec.CommandContext(ctx, "msiexec", "/x", path, "/qn", "/norestart").Run()
		_ = os.RemoveAll(root)
		_ = os.RemoveAll(filepath.Join(os.Getenv("ProgramFiles"), "MsiBuilderRemoveLeft"))

		if token != "" {
			_ = os.RemoveAll(filepath.Dir(backup))
			//nolint:gosec // the token comes from the test's own log
			_ = exec.CommandContext(ctx, "reg.exe", "delete", `HKU\S-1-5-18\Software\msi-builder\RemoveFoldersLeft\`+token, "/f").Run()
		}
	})

	writeFile(t, filepath.Join(data, `keep\k.txt`))

	if code := msiexecCode(t, filepath.Join(dir, "install.log"), "/i", path); code != 0 {
		t.Fatalf("install returned %d\n%s", code, readLog(filepath.Join(dir, "install.log")))
	}

	logPath := filepath.Join(dir, "rollback.log")
	if code := msiexecCode(t, logPath, "/x", path, "DATA_DIR="+data+`\`, "RECREATE=1"); code != 1603 {
		t.Fatalf("removal with RECREATE=1 returned %d, want 1603\n%s", code, readLog(logPath))
	}

	log := readLog(logPath)
	t.Logf("FILE-14: rolled-back removal:\n%s", grepLog(log, "BuilderRemoveFolders: "))

	match := leftLine.FindStringSubmatch(log)
	if match == nil {
		t.Fatal("the log does not name the backup folder the folder stays in")
	}

	backup, token = match[1], match[2]

	if !exists(filepath.Join(backup, `keep\k.txt`)) {
		t.Errorf("the moved folder is not at %s", backup)
	}

	//nolint:gosec // the token comes from the test's own log
	out, err := exec.CommandContext(t.Context(), "reg.exe", "query", `HKU\S-1-5-18\Software\msi-builder\RemoveFoldersLeft\`+token).CombinedOutput()
	if err != nil {
		t.Fatalf("the original path was not kept: %v\n%s", err, out)
	}

	t.Logf("FILE-14: kept record:\n%s", out)

	if text := string(out); !strings.Contains(text, "REG_SZ") || !strings.Contains(strings.ToLower(text), strings.ToLower(data+`\`)) || !strings.Contains(text, "ProductCode") {
		t.Errorf("the kept record lacks the original path or the product code:\n%s", text)
	}
}
