package config

import (
	"strings"

	"github.com/jkroepke/msi-builder/internal/condition"
)

// targetDir is the root of the directory tree.
const targetDir = "TARGETDIR"

// purge checks that a purged directory is a folder of the product: the
// action deletes whatever the directory property points to at uninstall.
func (v *validator) purge(cfg *Config, known *refs, field string, comp *Component) {
	if !comp.Purge.Enabled() {
		return
	}

	if keep := permanentBelow(cfg, comp.Directory); keep != "" {
		v.errorf(field+".purge", "permanent component %s outlives the product, but purge deletes its folder at "+
			"uninstall; keep permanent components out of the purged folder", keep)
	}

	v.purgeFolder(cfg, known, field, comp)

	// ICE105 keeps a dual package's actions impersonated, so in a
	// per-machine install a standard user's rmdir cannot delete there.
	if top := known.topFolder(comp.Directory); cfg.Package.Scope.Dual() && (perMachineFolders[top] || contextShellFolders[top]) {
		v.errorf(field+".purge", "the purge of a %s package runs as the user (ICE105 rejects elevated actions), "+
			"so a per-machine install could not delete under %s; purge a folder in the user profile, such as "+
			"LocalAppDataFolder/Vendor/Product", cfg.Package.Scope, top)
	}

	if comp.Purge.Condition != "" {
		if err := condition.Check(comp.Purge.Condition); err != nil {
			v.errorf(field+".purge.condition", "%v", err)
		}
	}
}

// purgeFolder rejects a purge of a folder that may not belong to the
// product: a standard folder, a folder that is its parent, or one below a
// folder a property can move.
func (v *validator) purgeFolder(cfg *Config, known *refs, field string, comp *Component) {
	dir := known.dirs[comp.Directory]

	switch {
	case dir == nil:
		// Reported for the directory already.
	case IsStandardFolder(comp.Directory) || known.dirParent[comp.Directory] == "":
		v.errorf(field+".purge", "%s is a standard folder; purge a folder below it", comp.Directory)
	case known.topFolder(comp.Directory) == targetDir:
		// TARGETDIR is public and falls back to ROOTDRIVE, the local drive
		// with the most free space (specification section 8.3).
		v.errorf(field+".purge", "%s is below TARGETDIR, whose drive can change; purge a folder below a fixed standard "+
			"folder, such as CommonAppDataFolder/Vendor/Product", comp.Directory)
	case dir.Name == "" || dir.Name == ".":
		v.errorf(field+".purge", "%s is its parent folder; purge a folder with its own name", comp.Directory)
	case unsafeNameInPath(known, comp.Directory) != "":
		v.errorf(field+".purge", "%s has a %% or ~ in its name: cmd.exe expands %%NAME%% as an environment variable, "+
			"and a name such as PROGRA~1 can be another folder's 8.3 alias; purge a folder whose path has neither",
			unsafeNameInPath(known, comp.Directory))
	default:
		if movable := movableAncestor(cfg, known, comp.Directory); movable != "" {
			v.errorf(field+".purge", "%s can be pointed at any folder (a public id, configurable, or set by an action, "+
				"a property or a dialog field), so the purge could delete a folder the product does not own; purge a "+
				"folder with private ids below a fixed standard folder, such as CommonAppDataFolder/Vendor/Product", movable)
		}
	}
}

// setProperties lists the properties that a properties entry, a
// setDirectory or setProperty action or a dialog field sets.
func setProperties(cfg *Config) map[string]bool {
	set := make(map[string]bool, len(cfg.Properties))

	for name := range cfg.Properties {
		set[name] = true
	}

	for _, ca := range cfg.CustomActions {
		switch {
		case ca.SetDirectory != nil:
			set[ca.SetDirectory.Directory] = true
		case ca.SetProperty != nil:
			set[ca.SetProperty.Name] = true
		}
	}

	for _, dialog := range cfg.UI.Dialogs {
		for _, field := range dialog.Fields {
			set[field.Property] = true
		}
	}

	return set
}

// movableAncestor returns the first directory from dirID up to its root
// folder whose path something besides the Directory
// table can set (specification section 8.3): a public id, which the command
// line can set; a configurable one; a standard folder nested below another
// one, which the installer resolves itself; or one named by a setDirectory or
// setProperty action, a properties entry or a dialog field. Only the last
// group applies to the root folder.
func movableAncestor(cfg *Config, known *refs, dirID string) string {
	set := setProperties(cfg)

	for range 64 {
		parent, ok := known.dirParent[dirID]
		if !ok {
			return ""
		}

		if parent == "" {
			// The root folder: the installer resolves a standard folder
			// itself, but an action or property can still move it.
			if set[dirID] {
				return dirID
			}

			return ""
		}

		dir := known.dirs[dirID]
		if dir != nil && dir.Configurable || set[dirID] || IsPublicProperty(dirID) || IsStandardFolder(dirID) {
			return dirID
		}

		dirID = parent
	}

	return ""
}

// unsafeNameInPath returns the first directory from dirID up to its root
// folder whose name has a %, which cmd.exe expands in the purge command line,
// or a ~, which can make it another folder's 8.3 alias.
func unsafeNameInPath(known *refs, dirID string) string {
	for range 64 {
		if dir := known.dirs[dirID]; dir != nil && strings.ContainsAny(dir.Name, "%~") {
			return dirID
		}

		parent, ok := known.dirParent[dirID]
		if !ok || parent == "" {
			return ""
		}

		dirID = parent
	}

	return ""
}

// permanentBelow returns the first permanent component whose folder is
// the folder of dirID or one below it. It compares default target paths
// (directoryPaths), not ids, because a "." alias is a second id for its
// parent: Root/./logs is the folder Root/logs.
func permanentBelow(cfg *Config, dirID string) string {
	paths := directoryPaths(cfg.Directories, "", make(map[string]string))

	purged, ok := paths[dirID]
	if !ok {
		return ""
	}

	for _, other := range cfg.Components {
		if other == nil || !other.Permanent {
			continue
		}

		folder, ok := paths[other.Directory]
		if !ok {
			// Reported for the directory already; compare ids.
			if other.Directory == dirID {
				return other.ID
			}

			continue
		}

		if strings.EqualFold(folder, purged) || strings.HasPrefix(strings.ToLower(folder), strings.ToLower(purged)+`\`) {
			return other.ID
		}
	}

	return ""
}
