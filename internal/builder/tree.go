package builder

import (
	"crypto/md5" //nolint:gosec // MsiFileHash is defined as MD5
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/jkroepke/msi-builder/internal/cab"
	"github.com/jkroepke/msi-builder/internal/msidb"
	"github.com/jkroepke/msi-builder/internal/pe"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// maxFileSize is the largest payload file: a file lies in one cabinet
// folder, which holds at most 65535 blocks of 32 KiB (section 5).
const maxFileSize = cab.MaxFolder

// Component attribute bits (section 8.4).
const (
	compSourceOnly        = 1
	compOptional          = 2
	compRegistryKeyPath   = 4
	compSharedDLLRefCount = 8
	compPermanent         = 16
	compTransitive        = 64
	compNeverOverwrite    = 128
	comp64Bit             = 256
)

// File attribute bits (section 11.5).
const (
	fileReadOnly = 0x1
	fileHidden   = 0x2
	fileSystem   = 0x4
	fileVital    = 0x200
	fileChecksum = 0x400
)

// standardFolderNames are the fixed 8.3 names of the standard folders
// (section 11.3, the reference profile).
var standardFolderNames = map[string]string{ //nolint:gochecknoglobals // lookup table
	"AdminToolsFolder": "Admin", "AppDataFolder": "AppData", "CommonAppDataFolder": "CommApp",
	"CommonFilesFolder": "CFiles", "CommonFiles64Folder": "CFiles64", "DesktopFolder": "Desktop",
	"FavoritesFolder": "Favs", "FontsFolder": "Fonts", "LocalAppDataFolder": "LocalApp",
	"MyPicturesFolder": "Pictures", "NetHoodFolder": "NetHood", "PersonalFolder": "Personal",
	"PrintHoodFolder": "Printers", "ProgramFilesFolder": "PFiles", "ProgramFiles64Folder": "PFiles64",
	"ProgramMenuFolder": "PMenu", "RecentFolder": "Recent", "SendToFolder": "SendTo",
	"StartMenuFolder": "StrtMenu", "StartupFolder": "StartUp", "SystemFolder": "System",
	"System16Folder": "System16", "System64Folder": "System64", "TempFolder": "Temp",
	"TemplateFolder": "Template", "WindowsFolder": "Windows", "WindowsVolume": ".",
}

func (b *build) identity() error {
	var err error

	b.upgradeCode, err = config.NormalizeGUID(b.cfg.Product.UpgradeCode)
	if err != nil {
		return fmt.Errorf("product.upgradeCode: %w", err)
	}

	b.version, err = config.ParseVersion(b.cfg.Product.Version)
	if err != nil {
		return fmt.Errorf("product.version: %w", err)
	}

	switch b.cfg.Product.ProductCode {
	case config.Auto:
		// Derived later, once every input is known (inputDigest).
	default:
		b.productCode, err = config.NormalizeGUID(b.cfg.Product.ProductCode)
		if err != nil {
			return fmt.Errorf("product.productCode: %w", err)
		}
	}

	if b.cfg.Product.PackageCode != config.Auto {
		b.packageCode, err = config.NormalizeGUID(b.cfg.Product.PackageCode)
		if err != nil {
			return fmt.Errorf("product.packageCode: %w", err)
		}
	}

	return nil
}

// pruneEmpty removes the mapping entries whose value means the same as an
// absent key (null, "", an empty list or mapping) and reports whether node
// itself is such a value. false and 0 stay: an explicit false or 0 can mean
// something else than a missing key (secure: false, checked: false), and a
// mapping that only becomes empty by pruning stays too (purge: true is
// {condition: ""}). List items are kept, because their position is part of
// the configuration. A new optional config field therefore needs a type
// whose unset value is pruned, or yaml:",omitempty".
func pruneEmpty(node *yaml.Node) bool {
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			pruneEmpty(child)
		}

		return false
	case yaml.MappingNode:
		return pruneMapping(node)
	case yaml.SequenceNode:
		for _, child := range node.Content {
			pruneEmpty(child)
		}

		return len(node.Content) == 0
	case yaml.ScalarNode:
		return node.Tag == "!!null" || node.Tag == "!!str" && node.Value == ""
	case yaml.AliasNode:
		return false
	}

	return false
}

// pruneMapping drops the entries of a mapping whose value pruneEmpty
// reports as empty, and reports whether the mapping was empty to begin with.
func pruneMapping(node *yaml.Node) bool {
	empty := len(node.Content) == 0
	kept := node.Content[:0]

	for i := 0; i+1 < len(node.Content); i += 2 {
		if !pruneEmpty(node.Content[i+1]) {
			kept = append(kept, node.Content[i], node.Content[i+1])
		}
	}

	node.Content = kept

	return empty
}

// deriveProductCode computes productCode: auto from the upgrade code, the
// platform, the version and a digest of the configuration and every input
// file, so that changed content gives a new code (UPG-03) and unchanged
// content the same one.
func (b *build) deriveProductCode() error { //nolint:cyclop // one pass over the format; splitting would scatter it
	if b.productCode != "" {
		return nil
	}

	cfgCopy := *b.cfg
	cfgCopy.BaseDir = ""
	// The template has written the variables into the configuration
	// already; a variable it does not read changes nothing installed.
	cfgCopy.Vars = nil

	var tree yaml.Node
	if err := tree.Encode(&cfgCopy); err != nil {
		return fmt.Errorf("digest of the configuration: %w", err)
	}

	// Keys with an empty value are left out, so that a new optional key in
	// a later msi-builder keeps the code of an unchanged configuration.
	pruneEmpty(&tree)

	yamlBytes, err := yaml.Marshal(&tree)
	if err != nil {
		return fmt.Errorf("digest of the configuration: %w", err)
	}

	hasher := sha256.New()
	hasher.Write(yamlBytes)

	// Disabled has no YAML key, so the marshaled config does not show it.
	if b.cfg.Upgrade != nil && b.cfg.Upgrade.Disabled {
		hasher.Write([]byte("upgrade:disabled"))
	}

	for _, f := range b.files {
		hasher.Write([]byte(f.Key))
		hasher.Write(f.digest[:])
	}

	b.hashProfileKeyPaths(hasher)
	b.hashRemovableCarriers(hasher)

	// Icons, binaries (custom action DLLs) and binary cells of custom
	// tables are installed content too: a changed DLL with the same
	// ProductCode would be refused as "already installed" (error 1638).
	for _, name := range sortedStringKeys(b.cfg.Icons) {
		b.hashInput(hasher, "icon:"+name, b.cfg.Icons[name])
	}

	for _, name := range sortedStringKeys(b.cfg.Binaries) {
		b.hashInput(hasher, "binary:"+name, b.cfg.Binaries[name])
	}

	// Wizard pictures and licenses are shown, not installed, but a changed
	// license or picture is a different installer all the same.
	wizard := &b.cfg.UI
	b.hashInput(hasher, "ui:license", wizard.License)
	b.hashInput(hasher, "ui:banner", wizard.Theme.Banner)
	b.hashInput(hasher, "ui:dialog", wizard.Theme.Dialog)

	for _, name := range slices.Sorted(maps.Keys(wizard.Languages)) {
		if entry := wizard.Languages[name]; entry != nil {
			b.hashInput(hasher, "ui:license:"+name, entry.License)
		}
	}

	for _, custom := range b.cfg.Tables {
		for colIndex, col := range custom.Columns {
			if typ, err := msidb.ParseType(col.Type); err != nil || typ.Kind != msidb.KindBinary {
				continue
			}

			for rowIndex, row := range custom.Rows {
				if colIndex >= len(row) {
					continue
				}

				if path, ok := row[colIndex].(string); ok && path != "" {
					b.hashInput(hasher, fmt.Sprintf("table:%s[%d][%d]", custom.Name, rowIndex, colIndex), path)
				}
			}
		}
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	b.productCode = uuid5(namespaceProduct, "product|"+b.upgradeCode+"|"+b.cfg.Product.Version+"|"+string(b.cfg.Package.Platform)+"|"+digest)

	return nil
}

// hashInput writes label and the content of the input file at path (opened
// like every input, by openSource) to hasher. An unreadable file contributes
// only its label; the table loaders report the error with its YAML path
// later.
func (b *build) hashInput(hasher io.Writer, label, path string) {
	if path == "" {
		return
	}

	_, _ = io.WriteString(hasher, label+"\x00")

	file, err := openSource(b.cfg, nil, path)
	if err != nil {
		return
	}
	defer file.Close()

	_, _ = io.Copy(hasher, file)
}

func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}

// directories builds the Directory table.
func (b *build) directories() error {
	b.addDir(&dirInfo{id: targetDir, canonical: targetDir, standard: true})

	var walk func(ds []*config.Directory, parent *dirInfo)

	walk = func(ds []*config.Directory, parent *dirInfo) {
		for _, dir := range ds {
			if existing, dup := b.dirs[dir.ID]; dup {
				// A top-level TARGETDIR node only carries children for the
				// root the builder already added.
				if parent == nil && dir.ID == targetDir {
					walk(dir.Children, existing)
				}

				continue
			}

			info := newDirInfo(dir, parent)

			if dir.Configurable {
				b.secure[dir.ID] = true
			}

			b.addDir(info)
			walk(dir.Children, info)
		}
	}

	walk(b.cfg.Directories, nil)

	return nil
}

// newDirInfo resolves a directory node below parent, nil for a top-level
// node: a standard folder, a named folder, or a second id for the parent
// folder (DefaultDir ".").
func newDirInfo(dir *config.Directory, parent *dirInfo) *dirInfo {
	info := &dirInfo{id: dir.ID}

	switch {
	case parent == nil && config.IsStandardFolder(dir.ID):
		info.parent = targetDir
		info.standard = true
		info.canonical = dir.ID
	case parent == nil:
		info.parent = targetDir
		info.long = dir.Name
		info.canonical = "TARGETDIR\\" + strings.ToLower(dir.Name)
	case dir.Name == config.AliasName:
		info.parent = parent.id
		info.canonical = parent.canonical
	default:
		info.parent = parent.id
		info.long = dir.Name
		info.canonical = parent.canonical + "\\" + strings.ToLower(dir.Name)
	}

	return info
}

func (b *build) addDir(d *dirInfo) {
	b.dirs[d.id] = d
	b.dirOrder = append(b.dirOrder, d)
}

// ensureStandardDir adds a standard folder row when an action needs it as a
// working directory.
func (b *build) ensureStandardDir(id string) {
	if _, ok := b.dirs[id]; ok {
		return
	}

	b.addDir(&dirInfo{id: id, parent: targetDir, standard: true, canonical: id})
}

// writeDirectoryTable emits the rows with generated short names. Every
// directory is written: the engine resolves the whole chain. Directories
// with one long name in one folder, at any depth, are one folder, so they
// get one short name; with two, SHORTFILENAMES=1 would presumably split them
// (inferred from FILE-06 and FILE-07, not tested).
func (b *build) writeDirectoryTable() {
	tbl := b.table("Directory")
	names := make(map[string]string)

	for _, dir := range b.dirOrder {
		switch {
		case dir.id == targetDir:
			tbl.Add(targetDir, nil, "SourceDir")
		case dir.standard:
			name, ok := standardFolderNames[dir.id]
			if !ok {
				name = "."
			}

			tbl.Add(dir.id, dir.parent, name)
		case dir.long == "":
			tbl.Add(dir.id, dir.parent, config.AliasName)
		default:
			key := b.folderKey(dir.parent) + "\x1f" + strings.ToLower(dir.long)
			if _, ok := names[key]; !ok {
				names[key] = filename(dir.parent, dir.long, true, b.taken(dir.parent), config.ValidShortName)
			}

			tbl.Add(dir.id, dir.parent, names[key])
		}
	}
}

// writeComponents prepares the component records; rows are written after the
// files are read, because the key path and GUID may depend on them.
func (b *build) writeComponents() error { //nolint:cyclop // one pass over the format; splitting would scatter it
	for i, comp := range b.cfg.Components {
		info := &componentInfo{cfg: comp, index: i, dir: b.dirs[comp.Directory]}
		if info.dir == nil {
			return fmt.Errorf("%s: directory %s is not defined", comp.Path(i), comp.Directory)
		}

		// A component of shortcuts only installs nothing in its directory.
		if !shortcutsOnly(comp) {
			info.dir.used = true
		}

		switch comp.Bitness {
		case "64":
			info.is64 = true
		case "32":
			info.is64 = false
		default:
			info.is64 = b.is64
		}

		if info.is64 {
			info.attributes |= comp64Bit
		}

		if comp.Permanent {
			info.attributes |= compPermanent
		}

		if comp.Transitive {
			info.attributes |= compTransitive
		}

		if comp.NeverOverwrite {
			info.attributes |= compNeverOverwrite
		}

		if comp.SharedDLLRefCount {
			info.attributes |= compSharedDLLRefCount
		}

		b.components[comp.ID] = info
	}

	return nil
}

// inputs reads every payload file and derives version, language, hash and
// attributes, then writes the Component, File, MsiFileHash, CreateFolder,
// Registry, Environment and service tables.
func (b *build) inputs() error { //nolint:cyclop // one pass over the format; splitting would scatter it
	for _, c := range b.cfg.Components {
		info := b.components[c.ID]
		p := info.cfg.Path(info.index)

		for j, f := range c.Files {
			filePath := fmt.Sprintf("%s.files[%d]", p, j)

			file := b.readFile(filePath, f, info)
			if file == nil {
				continue
			}

			if _, dup := b.fileByKey[file.Key]; dup {
				b.errorf(filePath+".id", "file key %s is used twice", file.Key)

				continue
			}

			b.fileByKey[file.Key] = file
			b.files = append(b.files, file)
		}
	}

	if err := b.diags.Err(); err != nil {
		return err //nolint:wrapcheck // the diagnostics carry their own paths
	}

	// Companion files: Version names another file of the component, by key
	// or by the long name of a file without an id; the column holds the key.
	for _, info := range b.files {
		if v, ok := info.Version.(string); ok && info.cfg.Version != config.Auto && info.cfg.Version != config.None {
			if _, err := config.ParseVersion(v); err != nil {
				if other := config.FileByRef(info.Component.cfg.Files, v); other != nil && b.fileByKey[config.FileKey(other)] != nil {
					info.Version = config.FileKey(other)
				} else {
					b.errorf(info.Component.cfg.Path(info.Component.index)+".files", "companion file %s is not defined", v)
				}
			}
		}
	}

	return b.diags.Err() //nolint:wrapcheck // the diagnostics carry their own paths
}

func (b *build) readFile(diagPath string, cfgFile *config.File, comp *componentInfo) *fileInfo {
	var file *fileInfo

	if cached, ok := b.fileCache[cfgFile]; ok {
		copied := *cached
		copied.Component = comp
		file = &copied
	} else {
		file = b.readPayload(diagPath, cfgFile, comp)
		if file != nil && b.fileCache != nil {
			cached := *file
			b.fileCache[cfgFile] = &cached
		}
	}

	// Set here rather than in readPayload, so that a language build, which
	// takes its files from the cache, has the property too: without it
	// the language transform would delete it from the base (FILE-09).
	// The engine verifies the attribute only while MSICHECKCRCS is set; a
	// non-empty value from properties overrides it.
	if file != nil && file.Attrs&fileChecksum != 0 {
		b.properties["MSICHECKCRCS"] = "1"
	}

	return file
}

// parsePE reads the version resource of a payload, or returns a nil info
// for a file that is not a PE image. A PE image whose headers or resources
// cannot be read is an executable without a known version: it fails the
// build like an unversioned one unless the configuration accepts that or
// sets the version itself, because packaging it unversioned would let an
// upgrade remove it (FILE-01). The bool is false once that is reported.
func (b *build) parsePE(src io.ReaderAt, cfgFile *config.File, name, diagPath string) (*pe.Info, bool) {
	info, err := pe.Parse(src)

	switch {
	case err == nil:
		return info, true
	case errors.Is(err, pe.ErrNotPE):
		return nil, true
	case cfgFile.Version == config.Auto && !cfgFile.AllowUnversionedExecutable:
		b.errorf(diagPath+".source", "%s is a PE file whose version resource cannot be read (%v); an upgrade can delete it (FILE-01). "+
			"Fix the file, set its version, or set allowUnversionedExecutable: true if this is intended", name, err)
	default:
		b.warnf(diagPath+".source", "%s is a PE file whose version resource cannot be read: %v", name, err)
	}

	return &pe.Info{}, false
}

// readPayload reads a payload file: its size, hashes and version resource.
func (b *build) readPayload(diagPath string, cfgFile *config.File, comp *componentInfo) *fileInfo { //nolint:cyclop,gocognit // one pass over the format
	src, err := openSource(b.cfg, cfgFile.FS, cfgFile.Source)
	if err != nil {
		// The path error names the file.
		b.errorf(diagPath+".source", "%v", err)

		return nil
	}

	defer src.Close()

	stat, err := src.Stat()
	if err != nil {
		b.errorf(diagPath+".source", "cannot read %s: %v", cfgFile.Source, err)

		return nil
	}

	if !stat.Mode().IsRegular() {
		b.errorf(diagPath+".source", "%s is not a regular file", cfgFile.Source)

		return nil
	}

	if stat.Size() > maxFileSize {
		b.errorf(diagPath+".source", "%s is larger than %d bytes (2 GiB - 32 KiB), the most one cabinet holds", cfgFile.Source, maxFileSize)

		return nil
	}

	// pe.Parse needs random access, and a payload is never held in memory.
	peSource, ok := src.(io.ReaderAt)
	if !ok {
		b.errorf(diagPath+".source", "%s: files of the fs.FS must implement io.ReaderAt, as those of os.DirFS, embed.FS and fstest.MapFS do", cfgFile.Source)

		return nil
	}

	file := &fileInfo{
		Key:       config.FileKey(cfgFile),
		Component: comp,
		cfg:       cfgFile,
		open: func() (io.ReadCloser, error) {
			return openSource(b.cfg, cfgFile.FS, cfgFile.Source)
		},
		Size:     stat.Size(),
		LongName: config.FileLongName(cfgFile),
	}

	// One pass over the file gives the MsiFileHash MD5 and the SHA-256 that
	// goes into the product code, without holding the file in memory.
	md5Sum, sha := md5.New(), sha256.New() //nolint:gosec // MsiFileHash is defined as MD5

	read, err := io.Copy(io.MultiWriter(md5Sum, sha), src)
	if err != nil {
		b.errorf(diagPath+".source", "cannot read %s: %v", cfgFile.Source, err)

		return nil
	}

	if read != stat.Size() {
		b.errorf(diagPath+".source", "%s changed while it was read: %d bytes, expected %d", cfgFile.Source, read, stat.Size())

		return nil
	}

	copy(file.digest[:], sha.Sum(nil))

	info, readable := b.parsePE(peSource, cfgFile, file.LongName, diagPath)
	file.isPE = info != nil

	if info == nil {
		info = &pe.Info{}
	}

	switch cfgFile.Version {
	case config.Auto:
		if file.isPE { //nolint:nestif // reads best as one block
			if !info.HasVersion {
				// parsePE has reported an unreadable one.
				if readable && !cfgFile.AllowUnversionedExecutable {
					b.errorf(diagPath+".version", "%s is a PE file without a version resource; an upgrade can delete it (FILE-01). "+
						"Set allowUnversionedExecutable: true if this is intended", file.LongName)
				}
			} else {
				file.Version = info.FileVersion
			}

			if info.Machine == pe.MachineI386 && comp.is64 {
				b.warnf(diagPath+".source", "%s is a 32-bit executable in a 64-bit component", file.LongName)
			}
		}
	case config.None:
	default:
		file.Version = cfgFile.Version
	}

	switch cfgFile.Language {
	case config.Auto:
		if file.isPE && info.HasVersion && len(info.Languages) > 0 {
			file.Language = info.Language()
		}
	case config.None:
	default:
		file.Language = cfgFile.Language
	}

	if file.Version == nil {
		file.Hash = fileHash(md5Sum.Sum(nil), stat.Size())
	}

	if *cfgFile.Vital {
		file.Attrs |= fileVital
	}

	if cfgFile.Checksum {
		if file.isPE && info.HasChecksum {
			file.Attrs |= fileChecksum // readFile sets MSICHECKCRCS
		} else {
			b.warnf(diagPath+".checksum", "%s has no PE checksum to verify; the checksum attribute is not set", file.LongName)
		}
	}

	if cfgFile.ReadOnly {
		file.Attrs |= fileReadOnly
	}

	if cfgFile.Hidden {
		file.Attrs |= fileHidden
	}

	if cfgFile.System {
		file.Attrs |= fileSystem
	}

	return file
}

// fileHash computes the MsiFileHash words from the file's MD5: four
// little-endian int32, zeros for an empty file (FILE-02).
func fileHash(sum []byte, size int64) *[4]int32 {
	var out [4]int32

	if size == 0 {
		return &out
	}

	for i := range out {
		out[i] = int32(binary.LittleEndian.Uint32(sum[4*i:])) //nolint:gosec // the hash is stored as four signed words
	}

	return &out
}

// sequenceFiles orders the files (canonical directory, name, key), numbers
// them and writes the file-related tables and the Component table.
func (b *build) sequenceFiles() error { //nolint:cyclop // one pass over the format; splitting would scatter it
	slices.SortStableFunc(b.files, func(left, right *fileInfo) int {
		if left.Component.dir.canonical != right.Component.dir.canonical {
			return strings.Compare(left.Component.dir.canonical, right.Component.dir.canonical)
		}

		if left.LongName != right.LongName {
			return strings.Compare(left.LongName, right.LongName)
		}

		return strings.Compare(left.Key, right.Key)
	})

	// Files with one long name in one folder, from two directory keys or
	// two conditional components, are one file and get one short name.
	names := make(map[string]string)

	for i, file := range b.files {
		file.Sequence = i + 1

		dir := file.Component.dir.id
		long := configuredFileName(file)

		key := b.folderKey(dir) + "\x1f" + strings.ToLower(long)
		if _, ok := names[key]; !ok {
			names[key] = filename(dir, long, false, b.taken(dir), config.ValidShortName)
		}

		file.FileName = names[key]
	}

	if err := b.deriveProductCode(); err != nil {
		return err
	}

	// Components in config order; rows are sorted by msidb anyway.
	b.guids = make(map[any]string, len(b.cfg.Components))

	for _, comp := range b.cfg.Components {
		info := b.components[comp.ID]
		path := info.cfg.Path(info.index)

		if err := b.resolveKeyPath(path, info); err != nil {
			return err
		}

		// The carrier of its shortcuts is the component (shortcuts.go).
		if shortcutsOnly(comp) {
			continue
		}

		if err := b.componentGUID(path, info); err != nil {
			return err
		}

		// Two components with one ComponentId break the component rules
		// (ICE08), for example two conditional variants of one file with the
		// same automatic GUID.
		b.uniqueGUID(path, comp.ID, info.guid)

		// A component keyed on its folder creates it, also when a
		// generated registry value becomes its key path.
		info.createFolder = info.keyPath == nil || comp.CreateFolder

		b.profileKeyPath(info)
		b.table("Component").Add(comp.ID, info.guid, comp.Directory, info.attributes, componentCondition(comp), info.keyPath)

		if info.createFolder {
			b.table("CreateFolder").Add(comp.Directory, comp.ID)
		}

		b.writeRegistry(info)
		b.writeEnvironment(info)
		b.writeServices(info)
		b.controlServices(info)
		b.purgeAction(info)
	}

	for _, fi := range b.files {
		b.table("File").Add(fi.Key, fi.Component.cfg.ID, fi.FileName, int(fi.Size), fi.Version, fi.Language, fi.Attrs, fi.Sequence)

		if fi.Hash != nil {
			b.table("MsiFileHash").Add(fi.Key, 0, int(fi.Hash[0]), int(fi.Hash[1]), int(fi.Hash[2]), int(fi.Hash[3]))
		}
	}

	b.splitCabinets()
	b.writeMedia()
	b.writeSignatures()

	return nil
}

// resolveKeyPath picks the component's key path: the explicit one, else the
// first file, else the first registry value, else the first value of its
// own file types, else the directory.
func (b *build) resolveKeyPath(path string, info *componentInfo) error { //nolint:cyclop // one pass over the format; splitting would scatter it
	comp := info.cfg

	var files []*fileInfo

	for _, f := range b.files {
		if f.Component == info {
			files = append(files, f)
		}
	}

	byRef := func(ref string) *fileInfo {
		for _, f := range files {
			if f.Key == ref || f.LongName == ref {
				return f
			}
		}

		return nil
	}

	switch keyPath := comp.KeyPath; {
	case keyPath == "directory":
		info.keyPath = nil
	case keyPath == "":
		// The first file in the configuration, not in sequence order: the
		// validator checks that one, and a service names its executable.
		if len(files) > 0 {
			info.keyFile = files[max(0, slices.IndexFunc(files, func(f *fileInfo) bool { return f.cfg == comp.Files[0] }))]
		} else if len(comp.Registry) > 0 {
			info.keyPath = b.registryID(info, comp.Registry[0])
			info.attributes |= compRegistryKeyPath
		} else if id, _ := b.fileTypeKeyPath(comp); id != "" {
			info.keyPath = id
			info.attributes |= compRegistryKeyPath
		}
	case byRef(keyPath) != nil:
		info.keyFile = byRef(keyPath)
	default:
		for _, r := range comp.Registry {
			if r.ID == keyPath {
				info.keyPath = keyPath
				info.attributes |= compRegistryKeyPath
			}
		}

		if info.keyPath == nil {
			return fmt.Errorf("%s.keyPath: %q is neither a file nor a registry id", path, keyPath)
		}
	}

	if info.keyFile != nil {
		info.keyPath = info.keyFile.Key

		// A companion file never is a key path (section 8.6); a component
		// that gets an HKCU key path does not use it as one.
		if v, ok := info.keyFile.Version.(string); ok && !b.needsProfileKeyPath(comp) {
			if _, err := config.ParseVersion(v); err != nil {
				b.errorf(path+".keyPath", "the key file %s is a companion file of %s", info.keyFile.Key, v)
			}
		}
	}

	if len(comp.Services) > 0 && info.keyFile == nil {
		b.errorf(path+".services", "a component with services needs the service executable as its key file")
	}

	return nil
}

// componentGUID derives the component code: pinned, none, or UUIDv5 over the
// upgrade code, the bitness and the key path's canonical location.
func (b *build) componentGUID(path string, info *componentInfo) error { //nolint:cyclop // one pass over the format; splitting would scatter it
	comp := info.cfg

	switch comp.GUID {
	case config.None:
		info.guid = nil

		return nil
	case config.Auto:
	default:
		g, err := config.NormalizeGUID(comp.GUID)
		if err != nil {
			return fmt.Errorf("%s.guid: %w", path, err)
		}

		info.guid = g

		return nil
	}

	bitness := "32"
	if info.is64 {
		bitness = "64"
	}

	var location string

	switch {
	case comp.FirewallFeature() != "":
		// The generated component of a feature's firewall rules shares
		// the install dir with other components keyed on it.
		location = "firewall|" + comp.FirewallFeature()
	case info.keyFile != nil:
		location = "file|" + info.dir.canonical + "\\" + strings.ToLower(info.keyFile.LongName)
	case info.keyPath != nil:
		for _, r := range comp.Registry {
			if b.registryID(info, r) == info.keyPath {
				location = "registry|" + strings.ToLower(r.Root+"\\"+r.Key+"\\"+r.Name)
			}
		}

		if id, key := b.fileTypeKeyPath(comp); location == "" && id == info.keyPath {
			location = "filetype|" + key
		}
	default:
		location = "directory|" + info.dir.canonical

		// Components keyed on one folder would share a code. One that
		// only registers event sources takes its first source instead,
		// but only when an earlier component took the folder's code, so
		// packages that built before keep their component codes.
		if len(comp.EventLog) > 0 {
			if _, taken := b.guids[uuid5(namespaceComponent, b.upgradeCode+"|"+bitness+"|"+location)]; taken {
				e := comp.EventLog[0]
				location = "eventlog|" + strings.ToLower(e.Log+"\\"+e.Source)
			}
		}
	}

	info.guid = uuid5(namespaceComponent, b.upgradeCode+"|"+bitness+"|"+location)

	return nil
}

// features writes Feature and FeatureComponents.
func (b *build) features() error {
	tbl := b.table("Feature")
	featureComps := b.table("FeatureComponents")
	display := 0

	config.WalkFeatures(b.cfg.Features, func(feature, parent *config.Feature) {
		var parentID any
		if parent != nil {
			parentID = parent.ID
		}

		// Display orders features in the selection tree: even values show a
		// feature collapsed, odd values expanded and 0 hides it. Each feature
		// takes the next even/odd pair so parity never depends on the
		// display state of the features written before it.
		display += 2

		var disp any

		switch feature.Display {
		case "hidden":
			disp = 0
		case "expanded":
			disp = display + 1
		default:
			disp = display
		}

		attrs := 0

		if !*feature.AllowAbsent {
			attrs |= 16 // UIDisallowAbsent
		}

		if !feature.AllowAdvertise {
			attrs |= 8 // DisallowAdvertise
		}

		tbl.Add(feature.ID, parentID, nullable(feature.Title), nullable(feature.Description), disp, *feature.Level, nullable(feature.Directory), attrs)

		for _, cond := range feature.Conditions {
			b.table("Condition").Add(feature.ID, cond.Value(), cond.FullCondition(*feature.Level))
		}

		for _, component := range feature.Components {
			for _, id := range b.featureComponents(component.ID) {
				featureComps.Add(feature.ID, id)
			}
		}
	})

	return nil
}

// featureComponents lists a configured component with the components the
// builder split off it (shortcut and service start carriers),
// which belong to the same feature.
func (b *build) featureComponents(compID string) []string {
	ids := []string{compID}

	for _, carrier := range b.carriers[compID] {
		if carrier != compID {
			ids = append(ids, carrier)
		}
	}

	return ids
}

// uniqueGUID records the ComponentId of a component and reports one that
// another component already has.
func (b *build) uniqueGUID(path, compID string, guid any) {
	if guid == nil {
		return
	}

	if other, dup := b.guids[guid]; dup {
		b.errorf(path+".guid", "component %s has the same ComponentId %v as %s; set guid on one of them", compID, guid, other)

		return
	}

	b.guids[guid] = compID
}
