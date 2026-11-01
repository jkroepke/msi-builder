package builder

import (
	"io"
	"slices"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// profileKeyPath gives a component in the user profile an HKCU registry
// value under Software\[Manufacturer]\[ProductName] as its key path, as
// ICE38 asks: it writes the value for every profileCandidate and makes it
// the key path when needsProfileKeyPath says so. The value is written in
// every installation context, also when a dual package installs per
// machine (INST-04). It runs after componentGUID, so the ComponentId
// still follows the key file, registry value or folder the component had
// before, and a package keeps its component codes.
//
// With a late removeOld the value is written but the key file stays the
// key path: an upgrade that moved the key path to the value lost the
// files, and one that moved it back left the old version's values behind
// unless the new version owns them too (INST-04).
func (b *build) profileKeyPath(info *componentInfo) {
	comp := info.cfg
	if !b.profileCandidate(comp) {
		return
	}

	registry := generatedID("reg", "ProfileKeyPath", b.upgradeCode, comp.ID)
	valueName := generatedID("cmp", "ProfileKeyPath", b.upgradeCode, comp.ID)

	b.table("Registry").Add(registry, registryRoots[rootHKCU], `Software\[Manufacturer]\[ProductName]`, valueName, "#1", comp.ID)

	if b.lateRemoval() {
		return
	}

	info.keyPath = registry
	info.attributes |= compRegistryKeyPath
}

// needsProfileKeyPath reports whether the builder makes the HKCU value the
// key path of a component: a profileCandidate in a package without a late
// removeOld. The validator makes the same decision and warns about the
// components this leaves out.
func (b *build) needsProfileKeyPath(comp *config.Component) bool {
	return b.profileCandidate(comp) && !b.lateRemoval()
}

// profileCandidate reports whether a component below a profile folder
// leaves its key path to the builder and would not get an HKCU value
// anyway (keepsKeyPath lists the exceptions). An HKMU value counts as HKCU
// only in a per-user package: a dual package installed per machine writes
// it to HKLM.
func (b *build) profileCandidate(comp *config.Component) bool {
	if b.keepsKeyPath(comp) || shortcutsOnly(comp) || !config.IsProfileFolder(b.topFolder(comp.Directory)) {
		return false
	}

	if len(comp.Files) == 0 && len(comp.Registry) > 0 {
		root := comp.Registry[0].Root

		return root != rootHKCU && (root != rootHKMU || b.cfg.Package.Scope != config.ScopePerUser)
	}

	return true
}

// keepsKeyPath reports whether a component's key path must stay as the
// configuration or resolveKeyPath makes it: an explicit keyPath (a
// firewall carrier's is generated), services, which need their
// executable, neverOverwrite (also set by preserve) and sharedDllRefCount,
// which test the key path (reported), per-machine registry values, which
// an HKCU key path would mix with per-user data (ICE57).
func (b *build) keepsKeyPath(comp *config.Component) bool {
	explicit := comp.KeyPath != "" && comp.FirewallFeature() == ""
	if explicit || len(comp.Services) > 0 || comp.NeverOverwrite || comp.SharedDLLRefCount {
		return true
	}

	return b.machineRegistry(comp)
}

// machineRegistry reports whether a component writes or removes
// per-machine registry data, as the validator decides it: HKLM and HKU
// values and removals, event log sources and machine environment
// variables, and HKMU and HKCR values, removals and file types
// (filetypes.go) unless the package only installs per user.
func (b *build) machineRegistry(comp *config.Component) bool {
	perUser := b.cfg.Package.Scope == config.ScopePerUser

	if len(comp.EventLog) > 0 {
		return true
	}

	roots := componentRoots(comp)

	return slices.ContainsFunc(roots, func(root string) bool {
		return root == rootHKLM || root == rootHKU || !perUser && (root == rootHKMU || root == rootHKCR)
	})
}

// hashProfileKeyPaths adds a marker to the ProductCode digest when a
// component gets an HKCU value (as its key path unless a late removeOld
// keeps the key file). A package built before msi-builder
// wrote these key paths (INST-04) differs from one built after, so the
// rebuild needs another code, or installing it over the old one fails
// with 1638.
func (b *build) hashProfileKeyPaths(hasher io.Writer) {
	if slices.ContainsFunc(b.cfg.Components, b.profileCandidate) {
		_, _ = hasher.Write([]byte("keypath:profile-hkcu"))
	}
}

// lateRemoval reports whether a major upgrade removes the old product
// after the new one has installed its files, as the validator decides it.
func (b *build) lateRemoval() bool {
	upg := b.cfg.Upgrade

	return upg != nil && !upg.Disabled && (upg.RemoveOld == config.PreserveRemoveOld || upg.RemoveOld == "afterInstallFinalize")
}

// componentRoots lists the registry roots a component writes to or
// removes from: its values and removals, HKCR for its file types and HKLM
// for its machine environment variables.
func componentRoots(comp *config.Component) []string {
	roots := make([]string, 0, len(comp.Registry)+len(comp.RemoveRegistry)+len(comp.Files))
	for _, reg := range comp.Registry {
		roots = append(roots, reg.Root)
	}

	for _, entry := range comp.RemoveRegistry {
		roots = append(roots, entry.Root)
	}

	for _, file := range comp.Files {
		if len(file.FileTypes) > 0 {
			roots = append(roots, rootHKCR)
		}
	}

	if len(comp.FileTypes) > 0 {
		roots = append(roots, rootHKCR)
	}

	for _, env := range comp.Environment {
		if env.Scope == envMachine {
			roots = append(roots, rootHKLM)
		}
	}

	return roots
}
