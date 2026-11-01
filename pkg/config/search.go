package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SearchFoundProperty returns the public property that AppSearch fills for
// the search at index of a searched property, before an action copies it
// over: <NAME>_FOUND for the first, <NAME>_FOUND2 for the second, and so on.
func SearchFoundProperty(name string, index int) string {
	suffix := "_FOUND"
	if index > 0 {
		suffix += strconv.Itoa(index + 1)
	}

	if len(name)+len(suffix) <= MaxIdentifierLength {
		return name + suffix
	}

	sum := sha256.Sum256([]byte(name))

	return strings.TrimPrefix(suffix, "_") + "_" + strings.ToUpper(hex.EncodeToString(sum[:16]))
}

// Search kinds.
const (
	searchKindRegistry  = "registry"
	searchKindDirectory = "directory"
	searchKindFile      = "file"
	searchKindComponent = "component"
)

// Kind returns the one field that is set, or "" when none or several are.
func (s *Search) Kind() string {
	var kinds []string

	if s.Registry != nil {
		kinds = append(kinds, searchKindRegistry)
	}

	if s.Directory != nil {
		kinds = append(kinds, searchKindDirectory)
	}

	if s.File != nil {
		kinds = append(kinds, searchKindFile)
	}

	if s.Component != nil {
		kinds = append(kinds, searchKindComponent)
	}

	if len(kinds) != 1 {
		return ""
	}

	return kinds[0]
}

// maxSearchNesting bounds how many under searches can be stacked.
const maxSearchNesting = 8

// Limits of the Signature and DrLocator columns.
const (
	maxSearchText    = 255
	maxSearchVersion = 20
	maxSearchDepth   = 32767
	maxSearchSize    = 2147483647
)

// The years a Signature row can hold: MinDate and MaxDate are positive
// 32-bit numbers with the year offset from 1980 in the top seven bits, and
// the sign bit is the highest of them.
const (
	minSearchYear = 1980
	maxSearchYear = 2043
)

// searchFileName rejects the characters a file name cannot hold.
var searchFileName = regexp.MustCompile(`^[^\\/:*?"<>|]+$`)

// IsFullSearchPath reports whether a DrLocator path is a full path: one
// that starts with a drive, a share or a property in brackets.
func IsFullSearchPath(p string) bool {
	return strings.HasPrefix(p, "[") || strings.HasPrefix(p, `\\`) ||
		len(p) >= 3 && p[1] == ':' && p[2] == '\\' && (p[0]|0x20 >= 'a' && p[0]|0x20 <= 'z')
}

// SearchDate converts a minDate or maxDate (YYYY-MM-DD) to the packed
// MS-DOS date and time of a Signature row. The engine compares the time of
// day too (SEARCH-05), so a minDate starts at 00:00:00 and a maxDate
// (endOfDay) ends at 23:59:58, the last time the format holds, and both
// bounds include the day they name.
func SearchDate(text string, endOfDay bool) (int, error) {
	date, err := time.Parse(time.DateOnly, text)
	if err != nil {
		return 0, fmt.Errorf("%q is not a date (YYYY-MM-DD)", text)
	}

	if date.Year() < minSearchYear || date.Year() > maxSearchYear {
		return 0, fmt.Errorf("%s is outside 1980-01-01 to 2043-12-31, the dates a Signature row can hold", text)
	}

	packed := ((date.Year()-1980)*512 + int(date.Month())*32 + date.Day()) << 16
	if endOfDay {
		// 23 hours, 59 minutes and 58 seconds in two-second units.
		packed |= 23<<11 | 59<<5 | 29
	}

	return packed, nil
}

// SearchVersion checks a minVersion or maxVersion: one to four numbers of
// up to 65535.
func SearchVersion(text string) error {
	parts := strings.Split(text, ".")
	if len(parts) > 4 {
		return fmt.Errorf("version %q has more than four fields", text)
	}

	for i, p := range parts {
		num, err := strconv.Atoi(p)
		if err != nil || strings.Trim(p, "0123456789") != "" || num > 0xFFFF {
			return fmt.Errorf("version %q: field %d is not a number up to 65535", text, i+1)
		}
	}

	return nil
}

// SearchMinVersion returns the Signature.MinVersion that finds files of
// at least version text in any language. The engine finds a file whose
// version equals MinVersion only when Signature.Languages lists the file's
// languages (SEARCH-05), so without languages the bound is lowered by one
// in the last field: 1.2.3.4 becomes 1.2.3.3, 1.2 becomes 1.1.65535.65535.
// 0.0.0.0 stays, as it cannot go lower; SEARCH-05 saw a MinVersion of
// 0.0.0.1 rule out a file without a version.
func SearchMinVersion(text string, languages []int) string {
	if len(languages) > 0 {
		return text
	}

	value := searchVersionValue(text)

	if value > 0 {
		value--
	}

	return fmt.Sprintf("%d.%d.%d.%d", value>>48, value>>32&0xFFFF, value>>16&0xFFFF, value&0xFFFF)
}

// searchVersionValue packs a version of up to four fields into 64 bits;
// missing fields count as 0.
func searchVersionValue(text string) uint64 {
	parts := strings.Split(text, ".")

	var value uint64

	for i := range 4 {
		field := 0
		if i < len(parts) {
			field, _ = strconv.Atoi(parts[i])
		}

		value = value<<16 | uint64(field)
	}

	return value
}

// propertySearches checks the searches of a property.
func (v *validator) propertySearches(cfg *Config, propPath, name string, searches Searches) {
	searchPath := propPath + ".search"

	v.searchedProperty(cfg, propPath, name)

	if len(searches) == 0 {
		v.errorf(searchPath, "at least one search is required")
	}

	for i, search := range searches {
		itemPath := searchPath
		if len(searches) > 1 {
			itemPath = fmt.Sprintf("%s[%d]", searchPath, i)
		}

		if found := SearchFoundProperty(name, i); cfg.Properties[found] != nil {
			v.errorf(itemPath, "%s holds the search result; it cannot also be defined in properties", found)
		}

		if search == nil {
			v.errorf(itemPath, "a search must not be empty")

			continue
		}

		v.search(cfg, itemPath, search, 0)
	}
}

// searchedProperty checks that a property can be searched.
func (v *validator) searchedProperty(cfg *Config, propPath, name string) {
	searchPath := propPath + ".search"

	if !IsPublicProperty(name) {
		v.errorf(searchPath, "only a public (upper-case) property can be searched, so that a command-line value and "+
			"the result reach the execute sequence")
	}

	if cfg.InstallDir.Remember && name == cfg.InstallDir.ID {
		v.errorf(searchPath, "installDir.remember already searches %s", name)
	}

	if prop := cfg.Properties[name]; prop.Secure != nil && !*prop.Secure {
		v.errorf(propPath+".secure", "a searched property must be secure: after a UI sequence the execute sequence "+
			"does not search again and only receives secure properties")
	}
}

// search checks one search. A nested search (depth > 0) is the folder an
// under search looks in, so it must find a folder.
func (v *validator) search(cfg *Config, searchPath string, search *Search, depth int) {
	switch search.Kind() {
	case searchKindRegistry:
		v.registrySearch(cfg, searchPath+".registry", search.Registry, depth > 0)
	case searchKindDirectory:
		v.directorySearch(cfg, searchPath+".directory", search.Directory, depth)
	case searchKindFile:
		if depth > 0 {
			v.errorf(searchPath+".file", "under must find a folder: a registry search of type directory, "+
				"a directory or a component search")

			return
		}

		v.fileSearch(cfg, searchPath+".file", search.File)
	case searchKindComponent:
		v.componentSearch(searchPath+".component", search.Component)
	default:
		v.errorf(searchPath, "exactly one of registry, directory, file or component is required")
	}
}

// registrySearch checks the registry value a search reads. Under another
// search the value must name a folder.
func (v *validator) registrySearch(cfg *Config, regPath string, reg *RegistrySearch, folder bool) {
	// RegLocator has no HKMU root.
	v.oneOf(regPath+".root", reg.Root, "HKLM", "HKCU", "HKCR", "HKU")
	v.required(regPath+".key", reg.Key)

	if strings.HasPrefix(reg.Key, `\`) || strings.HasSuffix(reg.Key, `\`) {
		v.errorf(regPath+".key", "no leading or trailing backslash")
	}

	if reg.Bitness != "" {
		v.oneOf(regPath+".bitness", reg.Bitness, "32", "64")
	}

	if reg.Bitness == "64" && !cfg.Package.Platform.Is64Bit() {
		v.errorf(regPath+".bitness", "a 32-bit package cannot read the 64-bit registry view")
	}

	switch {
	case reg.Type == "":
	case folder && reg.Type != SearchDirectory:
		v.errorf(regPath+".type", "under must find a folder: leave type out or set it to directory")
	default:
		v.oneOf(regPath+".type", reg.Type, SearchValue, SearchDirectory)
	}
}

// searchFolder checks the folder of a directory or file search: a full
// path, or an under search and a path relative to its folder.
func (v *validator) searchFolder(cfg *Config, field, folder string, under *Search, depth int) {
	switch {
	case under == nil && folder == "":
		v.errorf(field+".path", "required: a full path, or under and a relative path")
	case under == nil && !IsFullSearchPath(folder):
		v.errorf(field+".path", "%q is not a full path (a drive, a share or a property in brackets); "+
			"use under to search below another search's folder", folder)
	case under != nil && IsFullSearchPath(folder):
		v.errorf(field+".path", "with under the path is relative to the folder under finds")
	}

	if strings.Contains(folder, "|") {
		v.errorf(field+".path", "no short|long names; the long name is enough")
	}

	if under != nil {
		if depth+1 >= maxSearchNesting {
			v.errorf(field+".under", "at most %d searches in one chain", maxSearchNesting)

			return
		}

		v.search(cfg, field+".under", under, depth+1)
	}

	v.searchPathProperties(cfg, field+".path", folder)
}

// searchPathProperties checks the properties a search path names.
// AppSearch formats the path when it runs: the package's own folders are
// resolved only later by CostFinalize, and a searched property only after
// AppSearch, by its apply action. A path that starts with an empty
// property is relative, and a relative DrLocator path without a parent
// searches every fixed drive (Microsoft documents this; not tested).
func (v *validator) searchPathProperties(cfg *Config, field, folder string) {
	if len(folder) > maxSearchText {
		v.errorf(field, "at most %d characters (DrLocator.Path)", maxSearchText)
	}

	dirs := map[string]bool{cfg.InstallDir.ID: true}

	var walk func([]*Directory)

	walk = func(list []*Directory) {
		for _, dir := range list {
			dirs[dir.ID] = true
			walk(dir.Children)
		}
	}
	walk(cfg.Directories)

	for i, match := range bracketProperty.FindAllStringSubmatchIndex(folder, -1) {
		name := folder[match[2]:match[3]]
		leading := i == 0 && match[0] == 0

		switch prop := cfg.Properties[name]; {
		case IsStandardFolder(name):
		case prop != nil && len(prop.Search) > 0:
			v.errorf(field, "[%s] is searched itself, and its result is set only after AppSearch; "+
				"search below it with under instead", name)
		case dirs[name]:
			v.warnf(field, "[%s] is a folder of this package; AppSearch runs before CostFinalize resolves it, "+
				"so the path is resolved without it unless the command line sets it", name)
		case leading:
			v.warnf(field, "[%s] is not a standard folder property; when it is empty as AppSearch runs, the path is "+
				"relative and the engine searches every fixed drive", name)
		}
	}
}

// bracketProperty matches [NAME] references in a search path.
var bracketProperty = regexp.MustCompile(`\[([A-Za-z_][A-Za-z0-9_.]*)\]`)

// directorySearch checks a folder search.
func (v *validator) directorySearch(cfg *Config, field string, dir *DirectorySearch, depth int) {
	v.searchFolder(cfg, field, dir.Path, dir.Under, depth)
	v.searchDepth(field, dir.Depth)

	if dir.Under != nil && dir.Path == "" {
		v.errorf(field+".path", "is required under another search; search the folder it names directly instead")
	}

	if dir.Depth > 0 && dir.Path == "" {
		v.errorf(field+".depth", "needs a path: depth says how deep below the path's parent its last folder may lie")
	}
}

// searchDepth checks the Depth of a DrLocator row.
func (v *validator) searchDepth(field string, depth int) {
	if depth < 0 || depth > maxSearchDepth {
		v.errorf(field+".depth", "must be 0 to %d", maxSearchDepth)
	}
}

// systemFolders are where Windows keeps its own files.
var systemFolders = regexp.MustCompile(`(?i)^\[(SystemFolder|System64Folder|System16Folder|WindowsFolder)\]|^[a-z]:\\windows(\\|$)`)

// fileSearch checks a file search and its bounds.
func (v *validator) fileSearch(cfg *Config, field string, file *FileSearch) {
	if v.required(field+".name", file.Name) && !searchFileName.MatchString(file.Name) {
		v.errorf(field+".name", "%q is not a file name: no folder and none of \\ / : * ? \" < > |", file.Name)
	}

	if len(file.Name) > maxSearchText {
		v.errorf(field+".name", "at most %d characters (Signature.FileName)", maxSearchText)
	}

	v.searchFolder(cfg, field, file.Path, file.Under, 0)
	v.searchDepth(field, file.Depth)
	v.fileSearchBounds(field, file)
}

// fileSearchBounds checks the version, size, date and language bounds of a
// file search.
func (v *validator) fileSearchBounds(field string, file *FileSearch) {
	v.fileSearchVersions(field, file)
	v.fileSearchSizesAndDates(field, file)
}

// fileSearchVersions checks the version and language bounds of a file search.
func (v *validator) fileSearchVersions(field string, file *FileSearch) { //nolint:cyclop // a flat list of independent checks
	if (file.MinVersion != "" || file.MaxVersion != "") && systemFolders.MatchString(searchBasePath(file.Path, file.Under)) {
		v.warnf(field, "the versions of Windows' own files are unreliable to a search, consistent with the reported "+
			"compatibility shim (a minVersion search found ucrtbase.dll in System64Folder only below its real version, SEARCH-02). "+
			"Search the registry value CurrentBuildNumber under HKLM\\SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion "+
			"and compare it in a condition; VersionNT and WindowsBuild stop at Windows 8.1 (603 and 9600, PROP-02)")
	}

	if len(file.Languages) > 0 && file.MinVersion == "" {
		v.warnf(field+".languages", "has no effect without minVersion: the engine compares languages only "+
			"when the file's version equals minVersion (Microsoft documents this; observed with minVersion, SEARCH-05)")
	}

	if file.MinVersion != "" && SearchVersion(file.MinVersion) == nil &&
		len(SearchMinVersion(file.MinVersion, file.Languages)) > maxSearchVersion {
		v.errorf(field+".minVersion", "%s is written as %s, longer than the %d characters of Signature.MinVersion",
			file.MinVersion, SearchMinVersion(file.MinVersion, file.Languages), maxSearchVersion)
	}

	if len(file.MaxVersion) > maxSearchVersion {
		v.errorf(field+".maxVersion", "at most %d characters (Signature.MaxVersion)", maxSearchVersion)
	}

	langs := make([]string, 0, len(file.Languages))
	for _, lang := range file.Languages {
		langs = append(langs, strconv.Itoa(lang))
	}

	if text := strings.Join(langs, ","); len(text) > maxSearchText {
		v.errorf(field+".languages", "at most %d characters as a list (Signature.Languages)", maxSearchText)
	}

	for key, text := range map[string]string{"minVersion": file.MinVersion, "maxVersion": file.MaxVersion} {
		if text != "" {
			if err := SearchVersion(text); err != nil {
				v.errorf(field+"."+key, "%v", err)
			}
		}
	}

	if file.MinVersion != "" && file.MaxVersion != "" && SearchVersion(file.MinVersion) == nil &&
		SearchVersion(file.MaxVersion) == nil && searchVersionValue(file.MaxVersion) < searchVersionValue(file.MinVersion) {
		v.errorf(field+".maxVersion", "is less than minVersion")
	}

	for i, lang := range file.Languages {
		if lang < 0 || lang > 0xFFFF {
			v.errorf(fmt.Sprintf("%s.languages[%d]", field, i), "%d is not a language ID", lang)
		}
	}
}

// fileSearchSizesAndDates checks the size and date bounds of a file search.
func (v *validator) fileSearchSizesAndDates(field string, file *FileSearch) { //nolint:cyclop // a flat list of independent checks
	for key, size := range map[string]*int64{"minSize": file.MinSize, "maxSize": file.MaxSize} {
		if size != nil && (*size < 0 || *size > maxSearchSize) {
			v.errorf(field+"."+key, "must be 0 to %d", maxSearchSize)
		}
	}

	if file.MinSize != nil && file.MaxSize != nil && *file.MinSize > *file.MaxSize {
		v.errorf(field+".maxSize", "is less than minSize")
	}

	for key, text := range map[string]string{"minDate": file.MinDate, "maxDate": file.MaxDate} {
		if text != "" {
			if _, err := SearchDate(text, key == "maxDate"); err != nil {
				v.errorf(field+"."+key, "%v", err)
			}
		}
	}

	if file.MinDate != "" && file.MaxDate != "" && file.MaxDate < file.MinDate {
		v.errorf(field+".maxDate", "is before minDate")
	}
}

// searchBasePath returns the full path a folder chain starts from: path
// itself, or the path of the innermost directory search under it.
func searchBasePath(path string, under *Search) string {
	for under != nil && under.Directory != nil {
		path, under = under.Directory.Path, under.Directory.Under
	}

	if under != nil {
		return ""
	}

	return path
}

// componentSearch checks a component search.
func (v *validator) componentSearch(field string, comp *ComponentSearch) {
	if v.required(field+".guid", comp.GUID) {
		v.guid(field+".guid", comp.GUID)
	}

	if comp.KeyPath != "" {
		v.oneOf(field+".keyPath", comp.KeyPath, SearchFile, SearchDirectory)
	}
}
