package config_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// TestShortcutsOnlyFields keeps the two copies of shortcutsOnly
// (pkg/config/shortcut.go and internal/builder/shortcuts.go) in step with
// Component: a new field fails this test until both copies decide whether
// it makes a component more than its shortcuts.
func TestShortcutsOnlyFields(t *testing.T) {
	t.Parallel()

	want := []string{
		"ID", "Directory", "GUID", "Files", "Registry", "RemoveRegistry", "RemoveFiles", "RemoveFolders", "Services",
		"ServiceControl", "RestartResources",
		"EventLog",
		"Environment", "Purge", "CreateFolder", "Condition", "Scope", "Permanent", "NeverOverwrite", "Preserve",
		"SharedDLLRefCount", "Transitive", "KeyPath", "Bitness", "FolderPermissions", "Shortcuts", "FileTypes",
	}

	var got []string

	for field := range reflect.TypeFor[config.Component]().Fields() {
		if field.IsExported() {
			got = append(got, field.Name)
		}
	}

	if !slices.Equal(got, want) {
		t.Errorf("Component fields %v, want %v; update both shortcutsOnly copies, then this list", got, want)
	}
}
