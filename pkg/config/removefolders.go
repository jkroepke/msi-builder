package config

import (
	"fmt"
	"strings"

	"github.com/jkroepke/msi-builder/internal/condition"
)

// removeFoldersPrefix starts the names the folder removal uses itself: its
// type-51 and deferred actions and their properties.
const removeFoldersPrefix = "BuilderRemoveFolders"

// componentRemoveFolders checks the folders a component removes with
// everything in them.
func (v *validator) componentRemoveFolders(cfg *Config, known *refs, compPath string, comp *Component) {
	seen := make(map[[2]string]bool)

	var set map[string]bool

	for j, entry := range comp.RemoveFolders {
		entryPath := fmt.Sprintf("%s.removeFolders[%d]", compPath, j)

		v.oneOf(entryPath+".on", entry.On, RemoveOnInstall, RemoveOnUninstall, RemoveOnBoth)

		v.removeFolderCondition(entryPath+".condition", entry.Condition)

		name := entry.Property
		if !v.removeFolderProperty(entryPath+".property", name) {
			continue
		}

		key := [2]string{name, entry.On}
		if seen[key] {
			v.errorf(entryPath, "this component already removes [%s] %s", name, entry.On)
		}

		seen[key] = true

		if set == nil {
			set = setProperties(cfg)
		}

		v.removeFolderSource(cfg, known, entryPath+".property", name, set)

		// The installer decides which files to copy before the folder is
		// moved away, so a repair or upgrade would lose the files it kept.
		if entry.On != RemoveOnUninstall && (name == cfg.InstallDir.ID || known.dirs[name] != nil) {
			v.errorf(entryPath+".on", "on: %s cannot remove %s, a folder of the package: a repair would move away the files "+
				"the installer keeps there; remove it on uninstall, or name a folder the package does not install to", entry.On, name)
		}
	}
}

// removeFolderProperty checks the property of a removeFolders entry and
// reports whether it names one the removal may read.
func (v *validator) removeFolderProperty(field, name string) bool {
	switch {
	case name == "":
		v.errorf(field, "required: the directory ID or property that holds the folder")
	case !IsIdentifier(name):
		v.errorf(field, "%q is not a property name", name)
	case strings.HasPrefix(name, removeFoldersPrefix):
		v.errorf(field, "%s is reserved for the folder removal itself", name)
	case IsStandardFolder(name) || name == targetDir:
		v.errorf(field, "%s is a standard folder, which is never removed; name a folder of the product", name)
	default:
		return true
	}

	return false
}

// removeFolderSource checks where the folder comes from at removal, when
// the action reads it after costing: a directory has its path, but an
// install dir chosen at install time is only known again when
// installDir.remember or a property search of its ID finds it; any other
// property needs something that sets it.
func (v *validator) removeFolderSource(cfg *Config, known *refs, field, name string, set map[string]bool) {
	switch {
	case name == cfg.InstallDir.ID || known.dirs[name] != nil:
		if moved := movableFolder(cfg, known, name); moved != "" {
			v.warnf(field, "at uninstall %s holds its default path, not one chosen at install time for %s; %s", name, moved,
				rememberHint(cfg, moved))
		}
	case !set[name]:
		v.warnf(field, "nothing in the package sets %s, so only the command line can name the folder; "+
			"use a property with a search that finds the folder again at uninstall", name)
	}
}

// movableFolder returns the directory, dirID itself or one above it, whose
// path the command line can change at install time without the uninstall
// knowing it again: the install dir without installDir.remember, or a
// configurable directory, which nothing remembers; "" when there is none.
// A property search of a directory id finds its folder again before costing,
// as installDir.remember does (which refuses a second search of the install
// dir), so the folders from there down keep their installed paths.
func movableFolder(cfg *Config, known *refs, dirID string) string {
	for cur, depth := dirID, 0; cur != "" && depth < 64; cur, depth = known.dirParent[cur], depth+1 {
		if prop := cfg.Properties[cur]; prop != nil && len(prop.Search) > 0 {
			return ""
		}

		if cur == cfg.InstallDir.ID {
			if cfg.InstallDir.Remember {
				return ""
			}

			return cur
		}

		if dir := known.dirs[cur]; dir != nil && dir.Configurable {
			return cur
		}
	}

	return ""
}

// rememberHint says how to keep the folder chosen for dirID.
func rememberHint(cfg *Config, dirID string) string {
	if dirID == cfg.InstallDir.ID {
		return "set installDir.remember: true to remove the folder the product was installed to"
	}

	return "remember it in the registry and name a property with a search that finds it again"
}

// removeFolderCondition checks the condition of a removeFolders entry, which
// becomes the condition of the type-51 actions that add the entry.
func (v *validator) removeFolderCondition(field, cond string) {
	switch {
	case cond == "":
	case strings.ContainsFunc(cond, func(r rune) bool { return r < 0x20 }):
		v.errorf(field, "the condition cannot contain line breaks or control characters")
	default:
		if err := condition.Check(cond); err != nil {
			v.errorf(field, "%v", err)
		}
	}
}
