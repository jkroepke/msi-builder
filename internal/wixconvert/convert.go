// Package wixconvert converts a WiX source file (.wxs) to an msi-builder
// YAML file. The conversion is best effort: it maps the elements that
// docs/Migrating from WiX.md describes and writes a "# TODO" comment for
// everything it cannot convert, such as preprocessor conditions, includes,
// bind variables, hand-built dialogs and extensions without a counterpart.
// The result is a starting point that a person reviews; it is not meant to
// build the same package byte for byte.
package wixconvert

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// Note is something the converter could not convert, with the line of the
// source file it comes from (0 when it has none).
type Note struct {
	Message string
	Line    int
}

func (n Note) String() string {
	if n.Line == 0 {
		return n.Message
	}

	return fmt.Sprintf("line %d: %s", n.Line, n.Message)
}

// Result is a converted file.
type Result struct {
	// YAML is the msi.yaml text.
	YAML []byte
	// Notes are the TODO comments in YAML, in source order.
	Notes []Note
	// Vars are the ${NAME} variables that YAML references and does not
	// define; the WiX file took them from wix build -d or the
	// environment, msi-builder build takes them with --var.
	Vars []string
}

// Options tune the conversion.
type Options struct {
	// Built is the package WiX built from the source, or nil. A component
	// whose Guid WiX generated keeps the code it has there, so that a major
	// upgrade from that package that removes it late keeps the files.
	Built *BuiltPackage
	// Exists reports whether a file exists at a slash path relative to
	// the source file, or is nil. With a built package, a file that two
	// Files globs into one folder match keeps its code in the glob whose
	// source folder holds it.
	Exists func(rel string) bool
	// Source names the input in the header comment, such as app.wxs.
	Source string
}

// Convert reads a .wxs file and returns the msi.yaml that describes the
// same package as far as msi-builder can express it.
func Convert(r io.Reader, opts Options) (*Result, error) {
	doc, err := parse(r)
	if err != nil {
		return nil, err
	}

	if !doc.root.is("Wix") {
		return nil, fmt.Errorf("the root element is <%s>, not <Wix>", doc.root.qname())
	}

	conv := newConverter(doc)
	conv.built = opts.Built
	conv.exists = opts.Exists

	if err := conv.index(); err != nil {
		return nil, err
	}

	conv.emit()

	text, err := conv.render(opts)
	if err != nil {
		return nil, err
	}

	notes := append([]Note(nil), conv.notes...)
	slices.SortStableFunc(notes, func(a, b Note) int { return cmp.Compare(a.Line, b.Line) })

	return &Result{YAML: text, Notes: notes, Vars: conv.undefinedVars()}, nil
}

// dirFileSource returns the FileSource of a Directory or DirectoryRef,
// with its variables filled in.
func (c *converter) dirFileSource(elm *elem) string {
	src := elm.attr("FileSource")
	if src == "" {
		src = elm.attr("src")
	}

	if src == "" {
		return ""
	}

	return strings.TrimRight(sourcePath(c.expand(elm, src)), "/")
}

// converter holds the indexed source file and the output.
type converter struct {
	compByID map[string]*comp
	built    *BuiltPackage
	claimed  map[string]bool // the built components a component took
	exists   func(rel string) bool
	// builtDirIndex holds the folders of the built package by ID.
	builtDirIndex map[string]*BuiltDirectory
	advertised    []int // the lines of advertised shortcuts
	doc           *document
	wixVars       map[string]string
	dirs          map[string]*dir
	wixUI         *elem
	out           *omap
	pkg           *elem
	major         *elem
	summary       *elem
	usedVars      map[string]bool
	groups        map[string]*group
	actions       map[string]*elem
	needDirs      map[string]bool
	renames       map[string]string
	installDir    *dir
	uiSet         string
	platform      string
	// scope is the package's install scope, as package.scope names it.
	scope        string
	launchAction string
	// doActions are the DoAction control events by the action they run.
	doActions map[string]*elem
	// otherSequences are the sequences without a counterpart, such as
	// AdvertiseExecuteSequence, that schedule an action, by action.
	otherSequences map[string]*elem
	properties     []*elem
	// progressTexts are the ProgressText elements, which become the
	// progress key of the action they name.
	progressTexts []*elem
	actionOrder   []*elem
	actionRefs    []*elem
	binaries      []*elem
	upgrades      []*elem
	media         []*elem
	launches      []*elem
	uiNav         []*elem
	errorRows     []*elem
	usedErrors    map[*elem]bool
	// firewallNames are the lines of the firewall exceptions by name.
	firewallNames map[string]int
	// newerClaimed is the property that detects newer versions.
	newerClaimed string
	// downgradeFrom is the element whose message became
	// upgrade.downgradeMessage: the MajorUpgrade or a folded launch condition.
	downgradeFrom *elem
	icons         []*elem
	closeApps     []*elem
	defines       [][2]string
	// defined are the define values with earlier defines filled in.
	defined     map[string]string
	sequences   []*elem
	setters     []*elem
	structure   map[string]func(c *converter, e *elem, sc scope)
	features    []*feature
	featureByID map[string]*feature
	// quietCommands are the command line sources of the quiet exec
	// actions, and consumed the sources they take in.
	quietCommands map[string]commandSource
	consumed      map[*elem]bool
	featureGrps   map[string]*featureGroup
	topGroupRefs  []string
	comps         []*comp
	notes         []Note
	header        []string
	dirOrder      []*dir
	files         []*fileRef
	pending       []pendingShortcut
	pendingTypes  []pendingType
	anonymous     int
	removeLine    int
	uiLine        int
	v3            bool
	// floorSeen is set once a hand-made major upgrade became
	// upgrade.minVersion.
	floorSeen bool
}

type dir struct {
	parent *dir
	// parentID is the Directory or DirectoryRef the directory sits in. The
	// parents are linked once every fragment is read, so a DirectoryRef
	// may come before the Directory it names.
	parentID string
	id       string
	name     string
	line     int
	std      bool
	// sub marks a folder that a component's Subdirectory made.
	sub bool
	// generated is set when WiX gives the folder an ID of its own: one
	// without an Id, or one a Subdirectory path makes.
	generated bool
}

type comp struct {
	e   *elem
	out *omap
	// files is the FileSource for its files without Source.
	files fileSource
	// folder is the folder source a Files glob became, whose files keep
	// the codes WiX gave them in its guids.
	folder *omap
	id     string
	dir    string
	// tied are the files of a folder source that another one with the same
	// folder adds too, so that neither keeps their code.
	tied []string
	// droppedValues are the HKCU key path values of a shortcut component
	// that msi-builder replaces with its own.
	droppedValues []string
	inFeature     bool
	dropIfEmpty   bool
	// generated is set when WiX generates the component's Guid, pinned
	// when the built package gave it one.
	generated bool
	pinned    bool
}

type group struct {
	members []member
}

// member is an entry of a feature or component group: a component or a
// reference to a component or group.
type member struct {
	comp  *comp
	ref   string // ComponentRef Id
	group string // ComponentGroupRef Id
}

type feature struct {
	e       *elem
	kids    []*feature
	members []member
	// groupRefs are the FeatureGroupRefs inside the feature.
	groupRefs []string
	// refLine is the line of the first FeatureRef to the feature.
	refLine int
}

// featureGroup is a FeatureGroup: the features and components that a
// FeatureGroupRef puts in its place.
type featureGroup struct {
	features  []*feature
	members   []member
	groupRefs []string
	used      bool
}

func newConverter(doc *document) *converter {
	return &converter{
		doc:            doc,
		dirs:           make(map[string]*dir),
		compByID:       make(map[string]*comp),
		groups:         make(map[string]*group),
		featureByID:    make(map[string]*feature),
		featureGrps:    make(map[string]*featureGroup),
		actions:        make(map[string]*elem),
		wixVars:        make(map[string]string),
		firewallNames:  make(map[string]int),
		renames:        make(map[string]string),
		doActions:      make(map[string]*elem),
		otherSequences: make(map[string]*elem),
		needDirs:       make(map[string]bool),
		usedVars:       make(map[string]bool),
		usedErrors:     make(map[*elem]bool),
		out:            newMap(),
		structure:      structureHandlers(),
	}
}

// todof records a note as a comment above key in target, above target itself when
// key is empty, or in the file header when target is nil. A note that was
// already recorded is not repeated.
func (c *converter) todof(target *omap, key string, line int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	note := Note{Line: line, Message: msg}

	if slices.Contains(c.notes, note) {
		return
	}

	c.notes = append(c.notes, note)

	text := "TODO: " + msg
	if line != 0 {
		text = fmt.Sprintf("TODO(line %d): %s", line, msg)
	}

	if target == nil {
		c.header = append(c.header, text)

		return
	}

	if key == "" {
		target.head = append(target.head, text)

		return
	}

	target.comment(key, text)
}

// index walks the file once and records the elements by kind.
func (c *converter) index() error {
	c.preprocessor()
	c.extensionAttrs()

	var packages []*elem

	for _, elm := range c.doc.root.kids {
		switch {
		case elm.is("Package"), elm.is("Product"):
			packages = append(packages, elm)
		case elm.is("Fragment"):
		case elm.is("Bundle"), elm.is("Module"), elm.is("Patch"), elm.is("PatchCreation"):
			return fmt.Errorf("line %d: <%s> is not a package; msi-builder builds packages (.msi) only", elm.line, elm.name)
		}
	}

	if len(packages) != 1 {
		return errors.New("the file needs exactly one <Package> (WiX v3: <Product>)")
	}

	c.pkg = packages[0]
	c.v3 = c.pkg.is("Product")

	// WiX fills in !(wix.X) at bind time, so a folder name the walk
	// expands may use a WixVariable that comes later in the file.
	c.collectWixVars(c.doc.root)

	for _, e := range c.doc.root.kids {
		switch {
		case e == c.pkg, e.is("Fragment"):
			c.walk(e, scope{})
		default:
			c.todof(nil, "", e.line, "<%s> has no counterpart", e.qname())
		}
	}

	c.linkDirs()
	c.resolveFeatures()
	c.chooseInstallDir()
	c.choosePlatform()

	return nil
}

// scope is where an element sits: its directory, component group and
// feature.
type scope struct {
	group        *group
	feature      *feature
	featureGroup *featureGroup
	dir          string
	// files is where WiX looks for the files without Source here.
	files fileSource
}

// fileSource is the FileSource that applies to the elements in a Directory
// or DirectoryRef. joined marks one that the names of nested folders were
// joined to, which convert only assumes WiX does.
type fileSource struct {
	path   string
	joined bool
}

// below returns the file source of a nested folder of the given name.
func (f fileSource) below(name string) fileSource {
	if f.path == "" || name == "" || name == "." {
		return f
	}

	return fileSource{path: f.path + "/" + name, joined: true}
}

// sourceIn returns the file source inside a Directory, DirectoryRef or
// StandardDirectory: its own FileSource, or the outer one below its name.
func (c *converter) sourceIn(elm *elem, outer fileSource) fileSource {
	if own := c.dirFileSource(elm); own != "" {
		return fileSource{path: own}
	}

	if !elm.is("Directory") {
		return fileSource{}
	}

	return outer.below(c.expand(elm, elm.attr("Name")))
}

func (c *converter) walk(parent *elem, within scope) {
	for _, elm := range parent.kids {
		if elm.ns != nsCore {
			c.walkExtension(elm)

			continue
		}

		if c.walkElem(elm, within) {
			continue
		}

		if hint, ok := unsupported[elm.name]; ok {
			c.todof(nil, "", elm.line, "<%s> is not supported: %s", elm.qname(), hint)

			continue
		}

		c.todof(nil, "", elm.line, "<%s> has no counterpart", elm.qname())
	}
}

// structureHandlers index directories, components, groups and features.
// They walk the elements they contain, so they are built by a function: a
// package variable would refer to itself through walk.
func structureHandlers() map[string]func(c *converter, e *elem, sc scope) {
	return map[string]func(c *converter, e *elem, sc scope){
		"StandardDirectory": func(c *converter, e *elem, sc scope) {
			c.addDir(e, "", true)
			sc.dir = e.attr("Id")
			sc.files = c.sourceIn(e, sc.files)
			c.walk(e, sc)
		},
		"Directory": func(c *converter, e *elem, sc scope) {
			sc.dir = c.addDir(e, sc.dir, false).id
			sc.files = c.sourceIn(e, sc.files)
			c.walk(e, sc)
		},
		"DirectoryRef": func(c *converter, e *elem, sc scope) {
			sc.dir = e.attr("Id")
			sc.files = c.sourceIn(e, sc.files)
			c.walk(e, sc)
		},
		"ComponentGroup": func(conv *converter, elm *elem, sc scope) {
			inner := scope{dir: sc.dir, files: sc.files, group: conv.group(elm.attr("Id"))}
			if folder := elm.attr(attrDirectory); folder != "" {
				inner.dir, inner.files = folder, fileSource{}
			}

			conv.walk(elm, inner)
		},
		"Component": (*converter).addComp,
		// WiX 4 lets a File or Files stand outside a Component; each one
		// is a component of its own.
		"File":              func(c *converter, e *elem, sc scope) { c.addComp(implicitComponent(e), sc) },
		"Files":             func(c *converter, e *elem, sc scope) { c.addComp(implicitComponent(e), sc) },
		"ComponentRef":      func(c *converter, e *elem, sc scope) { c.addMember(sc, member{ref: e.attr("Id")}) },
		"ComponentGroupRef": func(c *converter, e *elem, sc scope) { c.addMember(sc, member{group: e.attr("Id")}) },
		"Feature":           (*converter).addFeature,
		// A FeatureRef adds the components and features it contains to
		// the feature it names.
		"FeatureRef": func(conv *converter, elm *elem, within scope) {
			feat := conv.featureFor(elm.attr("Id"))
			if feat.refLine == 0 {
				feat.refLine = elm.line
			}

			conv.walk(elm, scope{dir: within.dir, files: within.files, feature: feat})
		},
		"FeatureGroup": func(c *converter, e *elem, sc scope) {
			c.walk(e, scope{dir: sc.dir, files: sc.files, featureGroup: c.featureGroup(e.attr("Id"))})
		},
		"FeatureGroupRef": func(conv *converter, e *elem, sc scope) {
			switch groupID := e.attr("Id"); {
			case sc.feature != nil:
				sc.feature.groupRefs = append(sc.feature.groupRefs, groupID)
			case sc.featureGroup != nil:
				sc.featureGroup.groupRefs = append(sc.featureGroup.groupRefs, groupID)
			default:
				conv.topGroupRefs = append(conv.topGroupRefs, groupID)
			}
		},
		// A PropertyRef only pulls a fragment in; the converter reads
		// every fragment of the file anyway.
		"PropertyRef": func(*converter, *elem, scope) {},
	}
}

// packageElems index the elements that belong to the package as a whole.
var packageElems = map[string]func(c *converter, e *elem){ //nolint:gochecknoglobals // dispatch table
	"Property": func(c *converter, e *elem) { c.properties = append(c.properties, e) },
	"CustomAction": func(c *converter, e *elem) {
		c.actions[e.attr("Id")] = e
		c.actionOrder = append(c.actionOrder, e)
	},
	"SetProperty":              func(c *converter, e *elem) { c.setters = append(c.setters, e) },
	"SetDirectory":             func(c *converter, e *elem) { c.setters = append(c.setters, e) },
	"InstallExecuteSequence":   func(c *converter, e *elem) { c.sequences = append(c.sequences, e) },
	"InstallUISequence":        func(c *converter, e *elem) { c.sequences = append(c.sequences, e) },
	"AdminExecuteSequence":     func(c *converter, e *elem) { c.sequences = append(c.sequences, e) },
	"AdminUISequence":          func(c *converter, e *elem) { c.sequences = append(c.sequences, e) },
	"AdvertiseExecuteSequence": func(c *converter, e *elem) { c.sequences = append(c.sequences, e) },
	"Icon":                     func(c *converter, e *elem) { c.icons = append(c.icons, e) },
	"Binary":                   func(c *converter, e *elem) { c.binaries = append(c.binaries, e) },
	"WixVariable":              func(c *converter, e *elem) { c.wixVars[e.attr("Id")] = e.attr("Value") },
	"UI":                       (*converter).walkUI,
	"UIRef":                    (*converter).uiRef,
	"CustomActionRef":          func(c *converter, e *elem) { c.actionRefs = append(c.actionRefs, e) },
	"MajorUpgrade":             func(c *converter, e *elem) { c.major = e },
	"Upgrade":                  func(c *converter, e *elem) { c.upgrades = append(c.upgrades, e) },
	"Media":                    func(c *converter, e *elem) { c.media = append(c.media, e) },
	"MediaTemplate":            func(c *converter, e *elem) { c.media = append(c.media, e) },
	"Launch":                   func(c *converter, e *elem) { c.launches = append(c.launches, e) },
	elemCondition:              func(c *converter, e *elem) { c.launches = append(c.launches, e) },
	"SummaryInformation":       func(c *converter, e *elem) { c.summary = e },
}

// walkElem indexes one core element and reports whether it knows it.
func (c *converter) walkElem(elm *elem, within scope) bool {
	// A feature's conditional levels (v3 Condition, v4 Level) are read by
	// the feature; a v3 Condition there is not a launch condition.
	if (elm.name == elemCondition || elm.name == "Level") && within.feature != nil {
		return true
	}

	if fn, ok := c.structure[elm.name]; ok {
		fn(c, elm, within)

		return true
	}

	if fn, ok := packageElems[elm.name]; ok {
		fn(c, elm)

		return true
	}

	// The v3 Package element is the summary information.
	if elm.name == "Package" && c.v3 {
		c.summary = elm

		return true
	}

	return false
}

func (c *converter) walkExtension(elm *elem) {
	switch {
	case elm.ns == nsUI && elm.name == "WixUI":
		c.wixUI = elm
	case elm.qname() == elemCloseApp:
		c.closeApps = append(c.closeApps, elm)
	default:
		c.todof(nil, "", elm.line, "<%s> has no counterpart", elm.qname())
	}
}

// anonymousDir starts the ID of a Directory without one.
const anonymousDir = "dir_line"

func (c *converter) addDir(elm *elem, parentID string, standard bool) *dir {
	dirID := elm.attr("Id")
	if dirID == "" {
		dirID = fmt.Sprintf("%s%d", anonymousDir, elm.line)
	}

	if d, ok := c.dirs[dirID]; ok {
		return d
	}

	folder := &dir{id: dirID, name: c.expand(elm, elm.attr("Name")), parentID: parentID, line: elm.line, generated: elm.attr("Id") == ""}

	if dirID == "TARGETDIR" || standard || isStandardFolder(dirID) {
		folder.std, folder.name, folder.parentID = true, "", ""
	}

	c.dirs[dirID] = folder
	c.dirOrder = append(c.dirOrder, folder)

	return folder
}

// keyFileID returns the Id of a component's key path, which WiX gives a
// component without an Id, or "" when WiX generates it.
func (c *converter) keyFileID(elm *elem) string {
	key := keyElem(elm)

	switch {
	case key == nil:
		return ""
	case key.has("Id") || !key.is("File"):
		return key.attr("Id")
	}

	// WiX gives a File without an Id its name, when that is an identifier
	// of at most 72 characters.
	name := c.literal(key.attr("Name"))
	if name == "" {
		source := strings.ReplaceAll(c.literal(key.attr("Source")), `\`, "/")
		name = source[strings.LastIndex(source, "/")+1:]
	}

	if config.IsIdentifier(name) && len(name) <= config.MaxIdentifierLength {
		return name
	}

	return ""
}

// keyElem returns a component's key path: the element marked KeyPath, also
// one inside a RegistryKey, else the first File.
func keyElem(elm *elem) *elem {
	var first *elem

	for _, kid := range elm.kids {
		switch {
		case kid.yes("KeyPath"):
			return kid
		case kid.is("RegistryKey"):
			if key := markedKey(kid); key != nil {
				return key
			}
		case kid.is("File") && first == nil:
			first = kid
		}
	}

	return first
}

// markedKey returns the element marked KeyPath below a RegistryKey.
func markedKey(elm *elem) *elem {
	for _, kid := range elm.kids {
		if kid.yes("KeyPath") {
			return kid
		}

		if key := markedKey(kid); key != nil {
			return key
		}
	}

	return nil
}

// subdir adds the folders of a Subdirectory path, such as bin\\x64, below a
// directory and returns the ID of the last one.
func (c *converter) subdir(parentID, path string, line int) string {
	if c.dirs[parentID] == nil && isStandardFolder(parentID) {
		folder := &dir{id: parentID, std: true, line: line}
		c.dirs[parentID] = folder
		c.dirOrder = append(c.dirOrder, folder)
	}

	for _, name := range strings.FieldsFunc(path, func(r rune) bool { return r == '\\' || r == '/' }) {
		dirID := parentID + "_" + idPart(name)
		if _, ok := c.dirs[dirID]; !ok {
			folder := &dir{id: dirID, name: name, parentID: parentID, line: line, sub: true, generated: true}
			c.dirs[dirID] = folder
			c.dirOrder = append(c.dirOrder, folder)
		}

		parentID = dirID
	}

	return parentID
}

// idPart makes a folder name usable in a directory ID.
func idPart(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}

		return '_'
	}, name)
}

// linkDirs links each directory to its parent.
func (c *converter) linkDirs() {
	for _, folder := range c.dirOrder {
		if folder.parentID != "" {
			folder.parent = c.dirs[folder.parentID]
		}
	}
}

func (c *converter) group(id string) *group {
	grp, ok := c.groups[id]
	if !ok {
		grp = &group{}
		c.groups[id] = grp
	}

	return grp
}

// implicitComponent wraps a File or Files outside a Component in the
// component WiX makes for it, which takes its Id, Directory and
// Subdirectory.
func implicitComponent(file *elem) *elem {
	component := &elem{attrs: make(map[string]string), ns: file.ns, space: file.space, name: "Component", kids: []*elem{file}, line: file.line}

	// WiX names the component after the file.
	for _, name := range []string{"Id", attrDirectory, "Subdirectory"} {
		if v, ok := file.attrs[name]; ok {
			component.attrs[name] = v
			component.order = append(component.order, name)
		}
	}

	return component
}

func (c *converter) addComp(elm *elem, within scope) {
	comp := &comp{e: elm, id: elm.attr("Id"), dir: elm.attr(attrDirectory)}
	if comp.id == "" {
		// Two files of one name in other folders would give two components
		// one Id; the later one gets a generated one.
		if id := c.keyFileID(elm); c.compByID[id] == nil {
			comp.id = id
		}
	}

	if comp.dir == "" {
		comp.dir, comp.files = within.dir, within.files
	}

	if sub := c.expand(elm, elm.attr("Subdirectory")); sub != "" {
		comp.dir = c.subdir(comp.dir, sub, elm.line)
		for _, part := range strings.FieldsFunc(sub, func(r rune) bool { return r == '\\' || r == '/' }) {
			comp.files = comp.files.below(part)
		}
	}

	c.comps = append(c.comps, comp)
	if comp.id != "" {
		c.compByID[comp.id] = comp
	}

	c.addMember(within, member{comp: comp})
}

func (c *converter) addMember(within scope, entry member) {
	switch {
	case within.feature != nil:
		within.feature.members = append(within.feature.members, entry)
	case within.featureGroup != nil:
		within.featureGroup.members = append(within.featureGroup.members, entry)
	case within.group != nil:
		within.group.members = append(within.group.members, entry)
	}
}

// collectWixVars records the WixVariables below elm.
func (c *converter) collectWixVars(elm *elem) {
	for _, kid := range elm.kids {
		if kid.is("WixVariable") {
			c.wixVars[kid.attr("Id")] = kid.attr("Value")
		}

		c.collectWixVars(kid)
	}
}

// preprocessor turns <?define?> into vars and notes everything else.
func (c *converter) preprocessor() { //nolint:cyclop // one branch per processing instruction
	for _, proc := range c.doc.procs {
		if proc.skipped {
			continue
		}

		switch proc.target {
		case "define":
			name, value, ok := strings.Cut(proc.inst, "=")
			if !ok {
				c.todof(nil, "", proc.line, "<?define %s?> has no value", proc.inst)

				continue
			}

			value = strings.Trim(strings.TrimSpace(value), `"`)
			c.defines = append(c.defines, [2]string{varName(strings.TrimSpace(name)), value})
		case "include":
			c.todof(nil, "", proc.line, "<?include %s?>: convert the included file and merge it in by hand", proc.inst)
		case "if", "ifdef", "ifndef":
			c.todof(nil, "", proc.line, "<?%s %s?> is not evaluated: only its first branch was converted", proc.target, proc.inst)
		case "elseif", "else":
			if proc.leftOut {
				c.todof(nil, "", proc.line, "the <?%s?> branch was left out; convert it by hand if your build takes it", strings.TrimSpace(proc.target+" "+proc.inst))
			}
		case "foreach":
			c.todof(nil, "", proc.line, "<?foreach %s?> is not expanded: its body was converted once", proc.inst)
		case "endif", "endforeach":
		default:
			c.todof(nil, "", proc.line, "<?%s %s?> has no counterpart", proc.target, proc.inst)
		}
	}
}

// extensionAttrs notes the extension attributes on WiX elements, once per
// element. The parser records them element by element.
func (c *converter) extensionAttrs() {
	attrs := c.doc.extAttrs

	for len(attrs) > 0 {
		first, names := attrs[0], []string{attrs[0].name}

		attrs = attrs[1:]
		for len(attrs) > 0 && attrs[0].line == first.line && attrs[0].elem == first.elem {
			names = append(names, attrs[0].name)
			attrs = attrs[1:]
		}

		c.todof(nil, "", first.line, "the extension attributes %s on <%s> have no counterpart", strings.Join(names, ", "), first.elem)
	}
}

// undefinedVars are the variables the output references and does not
// define, in name order.
func (c *converter) undefinedVars() []string {
	var undefined []string

	for _, name := range slices.Sorted(maps.Keys(c.usedVars)) {
		if !slices.ContainsFunc(c.defines, func(d [2]string) bool { return d[0] == name }) {
			undefined = append(undefined, name)
		}
	}

	return undefined
}

// render writes the output as YAML.
func (c *converter) render(opts Options) ([]byte, error) {
	source := opts.Source
	if source == "" {
		source = "a WiX file"
	}

	head := []string{
		"Converted from " + source + " by msi-builder convert.",
		"Review it, resolve the TODO comments, then run msi-builder validate.",
	}
	if undefined := c.undefinedVars(); len(undefined) > 0 {
		head = append(head, "WiX variables became ${NAME} references; pass them with --var NAME=value: "+strings.Join(undefined, ", ")+".")
	}

	head = append(head, c.header...)

	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{c.out.node()}}
	doc.Content[0].Content[0].HeadComment = commentText(head)

	var buf bytes.Buffer

	buf.WriteString("# yaml-language-server: $schema=https://raw.githubusercontent.com/jkroepke/msi-builder/main/pkg/config/schema.json\n")

	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)

	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	return buf.Bytes(), nil
}
