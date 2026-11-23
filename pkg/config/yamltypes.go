package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"
)

// Platform is the target architecture. Its value is the exact Template
// token: "Intel", "x64" or "Arm64".
type Platform string

// Platforms as written to the Summary Information Template.
const (
	PlatformX86   Platform = "Intel"
	PlatformX64   Platform = "x64"
	PlatformArm64 Platform = "Arm64"
)

var platformNames = map[string]Platform{ //nolint:gochecknoglobals // lookup table
	"x86":     PlatformX86,
	"intel":   PlatformX86,
	"x64":     PlatformX64,
	"amd64":   PlatformX64,
	"arm64":   PlatformArm64,
	"aarch64": PlatformArm64,
}

// UnmarshalYAML accepts the user-facing names in any case and stores the
// exact token, since the engine compares tokens case-sensitively (SUM-02).
func (p *Platform) UnmarshalYAML(node *yaml.Node) error {
	var text string
	if err := decodeNode(node, &text); err != nil {
		return err
	}

	v, ok := platformNames[strings.ToLower(strings.TrimSpace(text))]
	if !ok {
		return lineError(node.Line, fmt.Errorf("unknown platform %q, want x86, x64 or arm64", text))
	}

	*p = v

	return nil
}

// Is64Bit reports whether components get the 64-bit attribute.
func (p Platform) Is64Bit() bool {
	return p == PlatformX64 || p == PlatformArm64
}

// Scope is the install context.
type Scope string

// Install contexts. The two dual scopes write ALLUSERS=2 and let the user
// choose the context at install time; the first part of the name is the
// default (spec section 8.2, INST-02).
const (
	ScopePerMachine       Scope = "perMachine"
	ScopePerUser          Scope = "perUser"
	ScopePerUserOrMachine Scope = "perUserOrMachine"
	ScopePerMachineOrUser Scope = "perMachineOrUser"
)

// Dual reports whether the package can be installed in either context.
func (s Scope) Dual() bool {
	return s == ScopePerUserOrMachine || s == ScopePerMachineOrUser
}

// MayBePerUser reports whether the package can be installed per user, so
// that it must pass ICE105.
func (s Scope) MayBePerUser() bool {
	return s == ScopePerUser || s.Dual()
}

// UnmarshalYAML accepts the scope names in any case.
func (s *Scope) UnmarshalYAML(node *yaml.Node) error {
	var text string
	if err := decodeNode(node, &text); err != nil {
		return err
	}

	switch strings.ToLower(text) {
	case "permachine":
		*s = ScopePerMachine
	case "peruser":
		*s = ScopePerUser
	case "peruserormachine":
		*s = ScopePerUserOrMachine
	case "permachineoruser":
		*s = ScopePerMachineOrUser
	default:
		return lineError(node.Line, fmt.Errorf("unknown scope %q, want perMachine, perUser, perUserOrMachine or perMachineOrUser", text))
	}

	return nil
}

// StringList is a list that may be written as a single scalar.
type StringList []string

// UnmarshalYAML accepts a scalar or a sequence of scalars.
func (l *StringList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var s string
		if err := decodeNode(node, &s); err != nil {
			return err
		}

		*l = StringList{s}

		return nil
	}

	var list []string
	if err := decodeNode(node, &list); err != nil {
		return err
	}

	*l = list

	return nil
}

// Duration is a time.Duration written as "60s" or "1m".
type Duration time.Duration

// UnmarshalYAML parses a Go duration string.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := decodeNode(node, &s); err != nil {
		return err
	}

	v, err := time.ParseDuration(s)
	if err != nil {
		return lineError(node.Line, err)
	}

	*d = Duration(v)

	return nil
}

// recoveryRestart is the recovery action that restarts the service.
const recoveryRestart = "restart"

// UnmarshalYAML accepts a scalar action name or a one-key {restart: delay} map.
func (a *RecoveryAction) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		return decodeNode(node, &a.Action)
	}

	if node.Kind != yaml.MappingNode || len(node.Content) != 2 || node.Content[0].Value != recoveryRestart {
		return lineError(node.Line, errors.New("a recovery action is restart, none or {restart: <delay>}"))
	}

	// The decoder skips UnmarshalYAML for a null value, which would leave a 0 delay.
	if node.Content[1].Tag == tagNull {
		return lineError(node.Line, errors.New("a restart step needs a delay"))
	}

	var delay Duration
	if err := decodeNode(node.Content[1], &delay); err != nil {
		return err
	}

	a.Action = recoveryRestart
	a.Restart = &delay

	return nil
}

// MarshalYAML writes the form UnmarshalYAML reads, so that the product code
// derived from the marshalled config sees every action.
func (a RecoveryAction) MarshalYAML() (any, error) {
	if a.Restart == nil {
		return a.Action, nil
	}

	return map[string]string{recoveryRestart: time.Duration(*a.Restart).String()}, nil
}

// UnmarshalYAML accepts a boolean or a mapping.
func (c *Close) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var on bool
		if err := decodeNode(node, &on); err != nil {
			return lineError(node.Line, fmt.Errorf("close is true, false or a mapping: %s", decodeMessage(err)))
		}

		c.Disabled = !on

		return nil
	}

	type plain Close

	return decodeStrict(node, (*plain)(c))
}

// MarshalYAML writes false for a disabled close, so that the product code
// derived from the marshalled config tells it from an enabled one.
func (c Close) MarshalYAML() (any, error) {
	if c.Disabled {
		return false, nil
	}

	type plain Close

	return plain(c), nil
}

// UnmarshalYAML accepts a boolean or a mapping.
func (p *Purge) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var on bool
		if err := decodeNode(node, &on); err != nil {
			return lineError(node.Line, fmt.Errorf("purge is true, false or a mapping: %s", decodeMessage(err)))
		}

		p.Disabled = !on

		return nil
	}

	type plain Purge

	return decodeStrict(node, (*plain)(p))
}

// MarshalYAML writes false for a disabled purge, so that the product code
// derived from the marshalled config tells it from an enabled one.
func (p Purge) MarshalYAML() (any, error) {
	if p.Disabled {
		return false, nil
	}

	return map[string]string{"condition": p.Condition}, nil
}

// Property is one Property table entry and its flags.
type Property struct { //nolint:govet // field order is the YAML key order
	Value  string `yaml:"value"`
	Secure *bool  `yaml:"secure"`
	Hidden bool   `yaml:"hidden"`
	// Search fills the property from the target machine before the
	// install; a value from the command line wins, and Value is the
	// fallback when no search finds anything (SEARCH-04). The searches are
	// tried in order and the first that finds something wins.
	Search Searches `yaml:"search"`
}

// Searches are the searches of one property, in order. YAML takes one
// search or a list.
type Searches []*Search

// Search is where a property's value is looked up: exactly one of its
// fields is set.
type Search struct {
	Registry  *RegistrySearch  `yaml:"registry"`
	Directory *DirectorySearch `yaml:"directory"`
	File      *FileSearch      `yaml:"file"`
	Component *ComponentSearch `yaml:"component"`
}

// RegistrySearch reads a registry value through a RegLocator row.
type RegistrySearch struct {
	Root string `yaml:"root"`
	Key  string `yaml:"key"`
	// Name is the value to read; empty reads the key's default value.
	Name string `yaml:"name"`
	// Bitness selects the 32- or 64-bit registry view; empty selects the
	// package's own.
	Bitness string `yaml:"bitness"`
	// Type is value (default), the data with the engine's type prefix;
	// directory, the folder the data names when that folder exists; or
	// file, the folder that holds the file the data names when that
	// folder exists. A file search under it checks the file (SEARCH-10).
	Type string `yaml:"type"`
}

// DirectorySearch finds a folder through a DrLocator row. The result ends
// with a backslash.
type DirectorySearch struct { //nolint:govet // field order is the YAML key order
	// Path is a full path, or with Under a path relative to the folder
	// that search finds. It may name properties in brackets.
	Path string `yaml:"path"`
	// Under is the search that finds the folder Path is relative to: a
	// registry search of type directory, a directory or a component
	// search.
	Under *Search `yaml:"under"`
	// Depth is how many levels of subfolders between the parent of Path
	// and its last folder may lie: with 2, C:\a\b also finds C:\a\x\y\b.
	Depth int `yaml:"depth,omitempty"`
}

// FileSearch finds a file through a Signature row and a DrLocator row. The
// result is the file's full path.
type FileSearch struct { //nolint:govet // field order is the YAML key order
	// Name is the file name, without a folder.
	Name string `yaml:"name"`
	// Path is the folder to look in, as in DirectorySearch.Path.
	Path string `yaml:"path"`
	// Under is the search that finds the folder Path is relative to, as
	// in DirectorySearch.Under.
	Under *Search `yaml:"under"`
	// Depth is how many levels of subfolders below the folder are
	// searched as well; 0 searches the folder only.
	Depth int `yaml:"depth,omitempty"`
	// MinVersion and MaxVersion bound the file version (inclusive). The
	// engine finds a file whose version equals MinVersion only when
	// Languages lists its languages, so without Languages the builder
	// writes MinVersion minus one (SEARCH-05).
	MinVersion string `yaml:"minVersion"`
	MaxVersion string `yaml:"maxVersion"`
	// MinSize and MaxSize bound the size in bytes (inclusive).
	MinSize *int64 `yaml:"minSize"`
	MaxSize *int64 `yaml:"maxSize"`
	// MinDate and MaxDate bound the modification date, as YYYY-MM-DD.
	MinDate string `yaml:"minDate"`
	MaxDate string `yaml:"maxDate"`
	// Languages are the language IDs the file's version resource must
	// list; the engine compares them only when the version equals
	// MinVersion.
	Languages []int `yaml:"languages"`
}

// ComponentSearch finds the folder of an installed component's key path
// through a CompLocator row.
type ComponentSearch struct {
	// GUID is the component code, of this or of another product.
	GUID string `yaml:"guid"`
	// KeyPath is what the component's key path is: file (the default for
	// another product's component), which returns the folder of the key
	// file, or directory, which returns the key path folder itself. The
	// engine returns the parent of a folder key path for file and nothing
	// for a file key path for directory (SEARCH-05, SEARCH-08).
	// For a component of this package the builder knows it, and a value
	// that contradicts the component is an error.
	KeyPath string `yaml:"keyPath"`
}

// Registry search types, and the component search key path kinds.
const (
	SearchValue     = "value"
	SearchDirectory = "directory"
	SearchFile      = "file"
)

// Properties maps property names to their settings.
type Properties map[string]*Property

// UnmarshalYAML accepts "NAME: value" as a short form of "NAME: {value: value}".
func (p *Properties) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return lineError(node.Line, errors.New("properties must be a mapping"))
	}

	out := make(Properties, len(node.Content)/2)

	for i := 0; i < len(node.Content); i += 2 {
		name, value := node.Content[i].Value, node.Content[i+1]
		if _, dup := out[name]; dup {
			return lineError(node.Content[i].Line, fmt.Errorf("property %s defined twice", name))
		}

		prop := &Property{}

		switch value.Kind {
		case yaml.ScalarNode:
			if value.Tag != tagNull {
				prop.Value = value.Value
			}
		case yaml.MappingNode:
			if err := decodeStrict(value, prop); err != nil {
				return err
			}
		case yaml.DocumentNode, yaml.SequenceNode, yaml.AliasNode, yaml.StreamNode:
			return lineError(value.Line, fmt.Errorf("property %s must be a value or a mapping", name))
		}

		out[name] = prop
	}

	*p = out

	return nil
}

// Names returns the property names in sorted order.
func (p Properties) Names() []string {
	names := make([]string, 0, len(p))
	for name := range p {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}

// decodeStrict decodes node into v and rejects unknown keys. yaml.Node.Decode
// does not honour Decoder.KnownFields, so mappings decoded by hand ask for
// the check themselves.
func decodeStrict(node *yaml.Node, out any) error {
	return publicTypeNames(node.Load(out, yaml.WithV3Defaults(), yaml.WithKnownFields()))
}

// decodeNode decodes node into out like node.Decode, with the error as a
// *yaml.LoadErrors (see publicTypeNames).
func decodeNode(node *yaml.Node, out any) error {
	return publicTypeNames(node.Decode(out))
}

// decodeMessage returns the messages of a decoding error without the line
// numbers, for an error that names the line itself.
func decodeMessage(err error) string {
	var loadErrs *yaml.LoadErrors
	if !errors.As(publicTypeNames(err), &loadErrs) {
		return err.Error()
	}

	msgs := make([]string, len(loadErrs.Errors))
	for i, e := range loadErrs.Errors {
		msgs[i] = e.Message
	}

	return strings.Join(msgs, "; ")
}

// lineError reports err at line as one of the decoder's own errors. yaml.v4
// lists a returned *yaml.LoadErrors with the others; any other error gets the
// line of the node being decoded put in front, which is the wrong line for
// an entry inside it.
func lineError(line int, err error) error {
	return &yaml.LoadErrors{Errors: []*yaml.LoadError{
		yaml.NewLoadError(yaml.ConstructorStage, err.Error(), yaml.Mark{Line: line}, err),
	}}
}

// The UnmarshalYAML methods below take the decoder's unmarshal function
// instead of a node, so that the decoder's strict mode still rejects unknown
// keys and reports the right lines. Each tries the short form first.

type (
	rawComponent  Component
	rawUpgrade    Upgrade
	rawInstallDir InstallDir
	rawArgument   Argument
)

// publicTypeNames rewrites decoding errors that name the raw types above,
// so that messages say config.Component, not config.rawComponent, and
// returns a lone *yaml.LoadError as a *yaml.LoadErrors, which yaml.v4 lists
// with its own errors instead of putting a second line in front.
func publicTypeNames(err error) error {
	var loadErrs *yaml.LoadErrors
	if !errors.As(err, &loadErrs) {
		// A decoder failure that stops the decoding comes as one
		// *yaml.LoadError; listed as one of many it keeps its line.
		var loadErr *yaml.LoadError
		if !errors.As(err, &loadErr) {
			return err
		}

		// The resolver's errors carry no position; as a plain error the
		// message gets the line of the node being decoded instead of
		// "<unknown position>".
		if loadErr.Mark.Line == 0 {
			return &unplacedError{loadErr}
		}

		loadErrs = &yaml.LoadErrors{Errors: []*yaml.LoadError{loadErr}}
	}

	out := &yaml.LoadErrors{Errors: make([]*yaml.LoadError, len(loadErrs.Errors))}
	for i, e := range loadErrs.Errors {
		renamed := *e
		renamed.Message = rawTypeName.ReplaceAllString(e.Message, "config.$1")
		out.Errors[i] = &renamed
	}

	return out
}

// unplacedError is a decoder error without a position, shown as its bare
// message.
type unplacedError struct{ err *yaml.LoadError }

func (e *unplacedError) Error() string {
	return rawTypeName.ReplaceAllString(e.err.Message, "config.$1")
}

func (e *unplacedError) Unwrap() error { return e.err }

var rawTypeName = regexp.MustCompile(`config\.raw([A-Z]\w*)`)

// UnmarshalYAML accepts a component id as a reference, which is how
// features[].components names a component defined elsewhere.
func (c *Component) UnmarshalYAML(unmarshal func(any) error) error {
	var id string
	if unmarshal(&id) == nil {
		*c = Component{ID: id, ref: true}

		return nil
	}

	if err := unmarshal((*rawComponent)(c)); err != nil {
		return publicTypeNames(err)
	}

	c.defined = true

	return nil
}

// UnmarshalYAML accepts true for the standard policy and false for none.
func (u *Upgrade) UnmarshalYAML(unmarshal func(any) error) error {
	var enabled bool
	if unmarshal(&enabled) == nil {
		*u = Upgrade{Disabled: !enabled}

		return nil
	}

	return publicTypeNames(unmarshal((*rawUpgrade)(u)))
}

// UnmarshalYAML accepts the folder name alone.
func (d *InstallDir) UnmarshalYAML(unmarshal func(any) error) error {
	var name string
	if unmarshal(&name) == nil {
		*d = InstallDir{Name: name}

		return nil
	}

	return publicTypeNames(unmarshal((*rawInstallDir)(d)))
}

// UnmarshalYAML accepts a whole command line or a list of pieces.
func (a *Arguments) UnmarshalYAML(unmarshal func(any) error) error {
	var line string
	if unmarshal(&line) == nil {
		*a = Arguments{{Value: line}}

		return nil
	}

	var list []Argument
	if err := unmarshal(&list); err != nil {
		return publicTypeNames(err)
	}

	*a = list

	return nil
}

// UnmarshalYAML accepts a piece without a condition as a plain string.
func (a *Argument) UnmarshalYAML(unmarshal func(any) error) error {
	var value string
	if unmarshal(&value) == nil {
		*a = Argument{Value: value}

		return nil
	}

	return publicTypeNames(unmarshal((*rawArgument)(a)))
}

// UnmarshalYAML accepts one search or a list of them.
func (s *Searches) UnmarshalYAML(unmarshal func(any) error) error {
	var list []*Search

	listErr := unmarshal(&list)
	if listErr == nil {
		*s = list

		return nil
	}

	var one Search

	err := unmarshal(&one)
	if err == nil {
		*s = Searches{&one}

		return nil
	}

	// A mapping fails the list attempt as a whole; anything else is a
	// list whose entries the list attempt describes.
	if strings.Contains(listErr.Error(), "!!map into []") {
		return publicTypeNames(err)
	}

	return publicTypeNames(listErr)
}

// MarshalYAML writes a single search as a mapping, so that the product code
// derived from a configuration with one search does not change with the
// list form.
func (s Searches) MarshalYAML() (any, error) {
	if len(s) == 1 {
		return s[0], nil
	}

	return []*Search(s), nil
}

// UnmarshalYAML keeps a plain -0 in a row the integer 0: yaml.v4 resolves
// it to the float -0, where yaml.v3 gave an int. -0.0 stays a float.
func (t *Table) UnmarshalYAML(node *yaml.Node) error {
	type plain Table

	if err := decodeStrict(node, (*plain)(t)); err != nil {
		return err
	}

	if rows := mappingValue(node, "rows"); rows != nil {
		for i, row := range deref(rows).Content {
			if i < len(t.Rows) {
				intNegativeZero(t.Rows[i], deref(row))
			}
		}
	}

	return nil
}

// intNegativeZero sets the cells of row written as a plain -0 to 0.
func intNegativeZero(row []any, node *yaml.Node) {
	if len(node.Content) != len(row) {
		return
	}

	for j, cell := range node.Content {
		cell = deref(cell)
		if f, ok := row[j].(float64); ok && f == 0 && cell.Style == 0 && cell.Value == "-0" {
			row[j] = 0
		}
	}
}

// mappingValue returns the value of key in mapping node, or nil.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}

	return nil
}

// deref follows an alias node to its anchor.
func deref(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		return node.Alias
	}

	return node
}
