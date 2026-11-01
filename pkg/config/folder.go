package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jkroepke/msi-builder/internal/folderglob"
)

// folderSource is a component with a folder source that ExpandFolders
// replaced, kept so that Validate still checks it under its own path.
type folderSource struct {
	comp *Component
	path string
}

// isFolderSource reports whether a file source names a folder: it ends in
// a slash or a backslash.
func isFolderSource(src string) bool {
	return strings.HasSuffix(src, "/") || strings.HasSuffix(src, `\`)
}

// folderComponent reports whether one of the component's files is a folder
// source.
func folderComponent(comp *Component) bool {
	for _, file := range comp.Files {
		if file != nil && isFolderSource(file.Source) {
			return true
		}
	}

	return false
}

// isFolderComponentID reports whether id names a component with a folder
// source, before or after ExpandFolders replaced it.
func isFolderComponentID(cfg *Config, compID string) bool {
	// A component defined under the id, for example by a Go caller after
	// the expansion, wins.
	for _, comp := range cfg.Components {
		if comp != nil && comp.ID == compID {
			return folderComponent(comp)
		}
	}

	for _, folder := range cfg.folders {
		if folder.comp.ID == compID {
			return true
		}
	}

	return false
}

// directoryReported reports whether the folder component that comp was
// generated from already reports the checks of comp's directory: comp is
// still below the same standard folder with the same bitness.
func directoryReported(known *refs, comp *Component) bool {
	return comp.folder != nil && comp.Bitness == comp.folder.Bitness &&
		known.topFolder(comp.Directory) == known.topFolder(comp.folder.Directory)
}

// preserveReported reports whether the folder component that comp was
// generated from already reports the preserve and removeOld checks.
func preserveReported(comp *Component) bool {
	return comp.folder != nil && len(comp.Services) == 0 && comp.Preserve == comp.folder.Preserve &&
		comp.NeverOverwrite == comp.folder.NeverOverwrite && comp.Permanent == comp.folder.Permanent
}

// ExpandFolders replaces every component whose file is a folder source
// (a source that ends in "/" or "\") with one component per file below the
// folder, as AddTree adds them: subfolders become directories below the
// component's directory, and each file gets its own component, key path and
// GUID. The include and exclude patterns of the file pick the files. The
// generated ids start with the component's id and are derived from its
// directory, the source as written and the path below it, so the same
// folder gives the same ids in every build. The component's condition,
// bitness, permanent, neverOverwrite, preserve, sharedDllRefCount and
// transitive, and the file's attributes, vital, checksum, permissions and
// allowUnversionedExecutable carry over to every generated component and
// file, and the generated components take the component's place in
// Components and in its features.
//
// A relative source is read from the file's FS, else Config.FS, else the
// folder of the YAML file (BaseDir); an absolute one from the file system.
// A firewall rule cannot name such a component. It applies the defaults
// first. The builder calls it after a first Validate and validates the
// expanded config again; call it yourself to see the components a build
// would add. A folder that fails is reported and its
// component stays; the others are expanded.
func (c *Config) ExpandFolders() Diagnostics {
	ApplyDefaults(c)

	var diags Diagnostics

	expanded := make(map[string][]*Component)
	components := make([]*Component, 0, len(c.Components))

	for i, comp := range c.Components {
		if comp == nil || comp.ref || !folderComponent(comp) {
			components = append(components, comp)

			continue
		}

		compPath := comp.Path(i)

		added, unused, err := c.expandFolder(comp, compPath)
		if err != nil {
			diags = append(diags, Diagnostic{Severity: SeverityError, Path: compPath + ".files[0].source", Message: err.Error()})
			components = append(components, comp)

			continue
		}

		if c.holdsYAMLFile(comp.Files[0]) {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning, Path: compPath + ".files[0].source",
				Message: "the folder holds the YAML file, so a package built next to it is added by the next build; use a separate output folder",
			})
		}

		for _, rel := range unused {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning, Path: compPath + ".files[0].guids",
				Message: fmt.Sprintf("the folder source adds no file %s, so its guid is not used", rel),
			})
		}

		c.folders = append(c.folders, folderSource{comp: comp, path: compPath})
		expanded[comp.ID] = added
		components = append(components, added...)
	}

	if len(expanded) == 0 {
		return diags
	}

	c.pinComponentPaths()
	c.Components = components
	c.replaceFeatureRefs(expanded)
	ApplyDefaults(c)

	return diags
}

// pinComponentPaths keeps the YAML path of every component before the
// expanded folders move their index.
func (c *Config) pinComponentPaths() {
	for i, comp := range c.Components {
		if comp != nil && comp.origin == "" {
			comp.origin = comp.Path(i)
		}
	}
}

// replaceFeatureRefs puts the generated components in place of the
// components they were expanded from in every feature.
func (c *Config) replaceFeatureRefs(expanded map[string][]*Component) {
	WalkFeatures(c.Features, func(feature, _ *Feature) {
		refs := make([]*Component, 0, len(feature.Components))

		for _, ref := range feature.Components {
			added, ok := expanded[ref.ID]
			if !ok {
				refs = append(refs, ref)

				continue
			}

			for _, comp := range added {
				refs = append(refs, ComponentRef(comp.ID))
			}
		}

		feature.Components = refs
	})
}

// expandFolder returns the components of one folder source and the paths
// of its guids that it adds no file for.
func (c *Config) expandFolder(comp *Component, compPath string) ([]*Component, []string, error) {
	if len(comp.Files) != 1 {
		return nil, nil, errors.New("a folder source must be the only file of its component")
	}

	file := comp.Files[0]

	filter, err := folderglob.New(file.Include, file.Exclude)
	if err != nil {
		return nil, nil, fmt.Errorf("%w", err)
	}

	// The check of Validate, worded for YAML, for a caller that did not
	// run it.
	if findDirectory(c.Directories, comp.Directory) == nil {
		return nil, nil, fmt.Errorf("directory %q is not defined", comp.Directory)
	}

	prefix := comp.ID
	if len(prefix) > MaxTreePrefixLength {
		prefix = prefix[:MaxTreePrefixLength]
	}

	fsys, root, err := c.folderFS(file)
	if err != nil {
		return nil, nil, err
	}

	idRoot := folderIDRoot(file.Source)

	added, err := c.walkTree(fsys, root, idRoot, comp.Directory, prefix, filter)
	if err != nil {
		return nil, nil, errors.New(strings.TrimPrefix(err.Error(), "add tree: "))
	}

	rels := make([]string, 0, len(added))

	for _, generated := range added {
		rel := generated.Files[0].Source
		if root != "." {
			rel = strings.TrimPrefix(rel, root+"/")
		}

		// Diagnostics of a generated component name the folder's
		// component and the path below its source.
		generated.origin = fmt.Sprintf("%s{%s}", compPath, rel)
		generated.folder = comp
		copyFolderSettings(generated, comp)

		rels = append(rels, rel)
	}

	return added, pinFolderGUIDs(file, added, rels), nil
}

// pinFolderGUIDs gives the components generated from a folder source, whose
// paths below the folder are rels, the GUIDs that its guids list for them,
// and returns the paths of guids the folder did not add, as written and
// sorted.
func pinFolderGUIDs(file *File, added []*Component, rels []string) []string {
	if len(file.GUIDs) == 0 {
		return nil
	}

	// Validate rejects two paths that differ in case only.
	pending := make(map[string]string, len(file.GUIDs))
	for rel, guid := range file.GUIDs {
		pending[strings.ToLower(rel)] = guid
	}

	for i, generated := range added {
		key := strings.ToLower(rels[i])
		if guid, ok := pending[key]; ok {
			generated.GUID = guid

			delete(pending, key)
		}
	}

	unused := make([]string, 0, len(pending))

	for rel := range file.GUIDs {
		if _, ok := pending[strings.ToLower(rel)]; ok {
			unused = append(unused, rel)
		}
	}

	slices.Sort(unused)

	return unused
}

// copyFolderSettings copies the settings of a folder's component and file
// to a component generated from it.
func copyFolderSettings(generated, comp *Component) {
	generated.Condition = comp.Condition
	generated.Scope = comp.Scope
	generated.Permanent = comp.Permanent
	generated.NeverOverwrite = comp.NeverOverwrite
	generated.Preserve = comp.Preserve
	generated.SharedDLLRefCount = comp.SharedDLLRefCount
	generated.Transitive = comp.Transitive
	generated.Bitness = comp.Bitness

	folder, file := comp.Files[0], generated.Files[0]
	file.AllowUnversionedExecutable = folder.AllowUnversionedExecutable
	file.Vital = folder.Vital
	file.Checksum = folder.Checksum
	file.ReadOnly = folder.ReadOnly
	file.Hidden = folder.Hidden
	file.System = folder.System
	file.Permissions = folder.Permissions
}

// folderFS returns the file system and the root in it that a folder source
// is read from, in the order the builder opens files: an absolute source
// from the disk, a relative one from the file's FS, Config.FS or BaseDir. A
// folder on disk is opened as a file system of its own, so that a relative
// one may lie outside the YAML file's folder.
func (c *Config) folderFS(file *File) (fs.FS, string, error) {
	var dir string

	switch src := file.Source; {
	case filepath.IsAbs(src):
		dir = src
	case file.FS != nil:
		return file.FS, folderIDRoot(src), nil
	case c.FS != nil:
		return c.FS, folderIDRoot(src), nil
	default:
		dir = filepath.Join(c.BaseDir, filepath.FromSlash(folderIDRoot(src)))
	}

	// The walk would name the folder ".", so check it here by name.
	info, err := os.Stat(dir)

	switch {
	case err != nil:
		if pathErr := (*fs.PathError)(nil); errors.As(err, &pathErr) {
			err = pathErr.Err
		}

		return nil, "", fmt.Errorf("folder %s: %w", file.Source, err)
	case !info.IsDir():
		return nil, "", fmt.Errorf("%s is not a folder", file.Source)
	}

	return os.DirFS(dir), ".", nil
}

// holdsYAMLFile reports whether a folder source read from disk is the YAML
// file's folder or one above it.
func (c *Config) holdsYAMLFile(file *File) bool {
	src := file.Source
	if c.BaseDir == "" || !filepath.IsAbs(src) && (file.FS != nil || c.FS != nil) {
		return false
	}

	dir := filepath.FromSlash(folderIDRoot(src))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(c.BaseDir, dir)
	}

	// Load leaves BaseDir relative ("." for msi.yaml), which Rel cannot
	// compare with an absolute source.
	dir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}

	base, err := filepath.Abs(c.BaseDir)
	if err != nil {
		return false
	}

	rel, err := filepath.Rel(dir, base)

	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// folderIDRoot is the source as written in slash form, cleaned; the ids of
// the folder are derived from it, so they do not depend on where the build
// runs as long as the source is relative.
func folderIDRoot(src string) string {
	return path.Clean(strings.ReplaceAll(src, `\`, "/"))
}

// folderComponents checks the components that ExpandFolders replaced.
func (v *validator) folderComponents(cfg *Config, known *refs) {
	for _, folder := range cfg.folders {
		v.componentDirectory(cfg, folder.path, folder.comp, known)
		v.folderComponent(cfg, folder.path, folder.comp)
	}
}

// folderComponent checks a component with a folder source without reading
// the folder, which ExpandFolders does.
func (v *validator) folderComponent(cfg *Config, compPath string, comp *Component) {
	if len(comp.Files) != 1 {
		v.errorf(compPath+".files", "a folder source (a source that ends in /) must be the only file of its component")
	}

	if cfg.Package.LongFileNames != nil && !*cfg.Package.LongFileNames {
		v.errorf(compPath+".files", "longFileNames is false, which needs 8.3 file names, but the files of a folder source keep their own names")
	}

	v.oneOf(compPath+".bitness", comp.Bitness, Auto, "32", "64")

	if comp.Bitness == "64" && !cfg.Package.Platform.Is64Bit() {
		v.errorf(compPath+".bitness", "64-bit components need an x64 or arm64 package")
	}

	v.folderComponentFields(compPath, comp)
	v.preserve(cfg, compPath, comp)

	for j, file := range comp.Files {
		if file != nil && isFolderSource(file.Source) {
			v.folderFile(fmt.Sprintf("%s.files[%d]", compPath, j), file)
		}
	}
}

// folderComponentFields rejects the settings of a component with a folder
// source that cannot be copied to every generated component.
func (v *validator) folderComponentFields(compPath string, comp *Component) {
	if comp.GUID != "" && comp.GUID != Auto {
		v.errorf(compPath+".guid", "not allowed on a component with a folder source, which becomes one component per file; "+
			"give a file its GUID with guids on the folder source")
	}

	for _, field := range []struct {
		name string
		set  bool
	}{
		{"keyPath", comp.KeyPath != ""},
		{"registry", len(comp.Registry) > 0},
		{"removeRegistry", len(comp.RemoveRegistry) > 0},
		{"removeFiles", len(comp.RemoveFiles) > 0},
		{"removeFolders", len(comp.RemoveFolders) > 0},
		{"services", len(comp.Services) > 0},
		{"serviceControl", len(comp.ServiceControl) > 0},
		{"eventLog", len(comp.EventLog) > 0},
		{"environment", len(comp.Environment) > 0},
		{"purge", comp.Purge.Enabled()},
		{"createFolder", comp.CreateFolder},
		{"folderPermissions", comp.FolderPermissions != nil},
		{"shortcuts", len(comp.Shortcuts) > 0},
		{"fileTypes", len(comp.FileTypes) > 0},
	} {
		if field.set {
			v.errorf(compPath+"."+field.name, "not allowed on a component with a folder source, which becomes one component per file; "+
				"put it on a component of its own")
		}
	}
}

// folderFile checks the file entry of a folder source.
func (v *validator) folderFile(filePath string, file *File) {
	for _, field := range []struct {
		name string
		set  bool
	}{
		{"name", file.Name != ""},
		{"id", file.ID != ""},
		{"version", file.Version != "" && file.Version != Auto},
		{"language", file.Language != "" && file.Language != Auto},
		{"shortcuts", len(file.Shortcuts) > 0},
		{"fileTypes", len(file.FileTypes) > 0},
		{"close", file.Close.Enabled()},
	} {
		if field.set {
			v.errorf(filePath+"."+field.name, "not allowed on a folder source, which adds every file below it; "+
				"list the file on a component of its own and exclude it here")
		}
	}

	if strings.TrimSpace(strings.TrimRight(file.Source, `/\`)) == "" {
		v.errorf(filePath+".source", "the folder is empty; use ./ for the YAML file's folder")
	}

	v.folderPatterns(filePath+".include", file.Include)
	v.folderPatterns(filePath+".exclude", file.Exclude)
	v.folderGUIDs(filePath+".guids", file.GUIDs)
	v.permissions(filePath+".permissions", file.Permissions, permFile)
}

// filePatterns rejects include, exclude and guids on a file that is not a
// folder source.
func (v *validator) filePatterns(compPath string, comp *Component) {
	for fileIdx, file := range comp.Files {
		if file == nil {
			continue
		}

		if len(file.Include) > 0 || len(file.Exclude) > 0 {
			v.errorf(fmt.Sprintf("%s.files[%d]", compPath, fileIdx), "include and exclude need a folder source, a source that ends in /")
		}

		if len(file.GUIDs) > 0 {
			v.errorf(fmt.Sprintf("%s.files[%d].guids", compPath, fileIdx), "guids needs a folder source, a source that ends in /; "+
				"set guid on the component instead")
		}
	}
}

// folderGUIDs checks the guids of a folder source: each key is a slash path
// below the folder, no two keys differ in case only, and each value is a
// GUID of its own.
func (v *validator) folderGUIDs(mapPath string, guids map[string]string) {
	paths := make(map[string]string, len(guids))
	codes := make(map[string]string, len(guids))

	for _, rel := range slices.Sorted(maps.Keys(guids)) {
		key := fmt.Sprintf("%s[%q]", mapPath, rel)

		if err := checkFolderPath(rel); err != nil {
			v.errorf(key, "%v", err)
		}

		if other, ok := paths[strings.ToLower(rel)]; ok {
			v.errorf(key, "%s names the same file as %s; paths are matched ignoring case", rel, other)
		}

		paths[strings.ToLower(rel)] = rel

		guid, err := NormalizeGUID(guids[rel])
		if err != nil {
			v.errorf(key, "%v", err)

			continue
		}

		if other, ok := codes[guid]; ok {
			v.errorf(key, "%s has the same guid as %s; every file needs a code of its own", rel, other)
		}

		codes[guid] = rel
	}
}

// checkFolderPath checks a path of guids: a file below the folder source in
// slash form.
func checkFolderPath(rel string) error {
	switch {
	case strings.Contains(rel, `\`):
		return errors.New(`use "/" to separate folders; "\" is not allowed`)
	case strings.HasPrefix(rel, "/"):
		return errors.New("the path is relative to source; remove the leading /")
	}

	for seg := range strings.SplitSeq(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return errors.New(`the path of a file below the folder cannot contain an empty, "." or ".." folder`)
		}
	}

	return nil
}

// folderPatterns checks the glob patterns of an include or exclude list.
func (v *validator) folderPatterns(listPath string, patterns StringList) {
	for i, pattern := range patterns {
		if err := folderglob.Check(pattern); err != nil {
			v.errorf(fmt.Sprintf("%s[%d]", listPath, i), "%q: %v", pattern, err)
		}
	}
}
