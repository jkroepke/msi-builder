package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
	"unicode"
	"unicode/utf8"
)

// maxRendered caps the text a template writes, and separately the text that
// the values of vars write. Without it a template such as
// {{ range 1000000000 }} could fill the memory.
const maxRendered = 16 << 20

// errRenderedTooLarge is returned when a template writes more than
// maxRendered bytes.
var errRenderedTooLarge = fmt.Errorf("the template writes more than %d bytes", maxRendered)

// Render runs a file through Go's text/template, as Parse does before it
// reads the YAML, and returns the text that comes out. {{ .NAME }} reads a
// variable: from Options.Vars, then through Options.LookupEnv, then from
// the vars header (see header), whose values may use other variables the
// same way. A variable that is not set is an error. The functions are Go's
// built-in ones and sprig's (https://masterminds.github.io/sprig/), less
// the ones whose result changes from run to run or between operating
// systems, and with these of msi-builder's own:
//
//   - has "NAME": whether the variable is set, for what WiX does with
//     <?ifdef?>.
//   - quote S: S as a double-quoted YAML string, for a value that holds
//     YAML syntax such as ": " or a line break.
//   - split SEP S: the parts of S between each SEP as a list, for a range
//     over a list such as --var LANGS=en;de; an empty S has no parts, and
//     SEP must not be empty.
//
// The header is copied to the output as it is. The output is capped at
// 16 MiB, but a template is code: like a build script, only render files
// you trust, as a loop that writes nothing is not bounded.
func Render(data []byte, opts Options) ([]byte, error) {
	out, err := renderFile(data, opts)
	if err != nil {
		return nil, err
	}

	return out.text, nil
}

// rendered is a rendered file.
type rendered struct {
	// vars are the values of the header's vars.
	vars map[string]string
	// text is the header as it is and the rendered body.
	text []byte
	// body is the rendered body after blank lines in place of the header,
	// so that it has the lines of the file.
	body []byte
}

func renderFile(data []byte, opts Options) (*rendered, error) {
	vars := &varResolver{opts: opts, raw: make(map[string]string), lines: make(map[string]int), left: maxRendered}
	body, offset := data, 0

	hdr, err := splitHeader(data)
	if err != nil {
		return nil, err
	}

	if hdr != nil {
		if vars.raw, vars.lines, err = fileVars(hdr.root); err != nil {
			return nil, err
		}

		body, offset = data[len(hdr.text):], hdr.lines
	}

	tmpl, err := template.New("msi.yaml").Option("missingkey=error").Funcs(templateFuncs(vars)).Parse(string(body))
	if err != nil {
		return nil, fmt.Errorf("template: %w", templateErr(err, opts, offset))
	}

	values, err := vars.values(tmpl)
	if err != nil {
		return nil, err
	}

	out := &limitedBuffer{left: maxRendered}
	if err := tmpl.Execute(out, values); err != nil {
		return nil, vars.execErr(err, offset)
	}

	res := &rendered{body: append(bytes.Repeat([]byte("\n"), offset), out.Bytes()...), vars: values}
	if hdr != nil {
		res.text = append(slices.Clip(hdr.text), out.Bytes()...)
		res.vars = make(map[string]string, len(vars.raw))

		for name := range vars.raw {
			res.vars[name] = values[name]
		}
	} else {
		res.text = out.Bytes()
		res.vars = nil
	}

	return res, nil
}

// varResolver finds the variables of a template. The value of a header
// entry is itself a template that may read other variables; it is
// rendered once, when it is first needed.
type varResolver struct {
	opts  Options
	raw   map[string]string
	lines map[string]int
	done  map[string]string
	errs  map[string]error
	stack []string
	// left is what the values of vars may still write.
	left int
}

// defined reports whether name is set anywhere.
func (r *varResolver) defined(name string) bool {
	if _, ok := r.opts.Vars[name]; ok {
		return true
	}

	if r.opts.LookupEnv != nil {
		if _, ok := r.opts.LookupEnv(name); ok {
			return true
		}
	}

	_, ok := r.raw[name]

	return ok
}

// get returns the value of a variable that is defined.
func (r *varResolver) get(name string) (string, error) {
	if v, ok := r.opts.Vars[name]; ok {
		return v, nil
	}

	if r.opts.LookupEnv != nil {
		if v, ok := r.opts.LookupEnv(name); ok {
			return v, nil
		}
	}

	if v, ok := r.done[name]; ok {
		return v, nil
	}

	if err, ok := r.errs[name]; ok {
		return "", err
	}

	value, err := r.renderVar(name)
	if r.done == nil {
		r.done, r.errs = make(map[string]string), make(map[string]error)
	}

	if err != nil {
		r.errs[name] = err

		return "", err
	}

	r.done[name] = value

	return value, nil
}

// renderVar renders the value of the header entry name.
func (r *varResolver) renderVar(name string) (string, error) {
	where := fmt.Sprintf("line %d: vars.%s", r.lines[name], name)

	// The cycle is reported on the entry that closes it.
	if i := slices.Index(r.stack, name); i >= 0 {
		chain := strings.Join(append(slices.Clone(r.stack[i:]), name), " -> ")

		return "", fmt.Errorf("line %d: vars.%s: %s is a cycle", r.lines[r.stack[len(r.stack)-1]], r.stack[len(r.stack)-1], chain)
	}

	value := r.raw[name]
	if !strings.Contains(value, "{{") {
		return value, nil
	}

	r.stack = append(r.stack, name)
	defer func() { r.stack = r.stack[:len(r.stack)-1] }()

	tmpl, err := template.New("msi.yaml").Option("missingkey=error").Funcs(templateFuncs(r)).Parse(value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", where, templateErr(err, r.opts, 0))
	}

	data := make(map[string]string)

	for _, dep := range names(tmpl) {
		if !r.defined(dep) {
			continue
		}

		v, err := r.get(dep)
		if err != nil {
			return "", err
		}

		data[dep] = v
	}

	out := &limitedBuffer{left: r.left}
	if err := tmpl.Execute(out, data); err != nil {
		if errors.Is(err, errRenderedTooLarge) {
			return "", fmt.Errorf("vars: %w", errRenderedTooLarge)
		}

		return "", fmt.Errorf("%s: %w", where, templateErr(err, r.opts, 0))
	}

	r.left -= out.Len()

	if !utf8.Valid(out.Bytes()) {
		return "", fmt.Errorf("%s: the value is not valid UTF-8", where)
	}

	return out.String(), nil
}

// values returns the data of the file's template: every --var, every
// header entry and every environment variable the template reads, as far
// as they render without an error. A broken entry is reported only where
// the template reads it (see execErr), so one that a branch the build does
// not take reads is no error.
func (r *varResolver) values(tmpl *template.Template) (map[string]string, error) {
	values := maps.Clone(r.opts.Vars)
	if values == nil {
		values = make(map[string]string)
	}

	add := func(name string) {
		if _, ok := values[name]; ok || !r.defined(name) {
			return
		}

		if v, err := r.get(name); err == nil {
			values[name] = v
		}
	}

	// In order, so that a cycle is reported on the same entry every time.
	for _, name := range slices.Sorted(maps.Keys(r.raw)) {
		add(name)
	}

	for _, name := range names(tmpl) {
		add(name)
	}

	if r.left < 0 {
		return nil, fmt.Errorf("vars: %w", errRenderedTooLarge)
	}

	return values, nil
}

// execErr reports an error of the file's template; a variable that is
// missing because its header entry is broken reports the entry.
func (r *varResolver) execErr(err error, offset int) error {
	if errors.Is(err, errRenderedTooLarge) {
		return fmt.Errorf("template: %w", errRenderedTooLarge)
	}

	if m := missingKey.FindStringSubmatch(err.Error()); m != nil {
		if entryErr, ok := r.errs[m[1]]; ok {
			return entryErr
		}
	}

	return fmt.Errorf("template: %w", templateErr(err, r.opts, offset))
}

// names returns the variables that a template reads as .NAME or $.NAME.
func names(tmpl *template.Template) []string {
	var out []string

	for _, tree := range tmpl.Templates() {
		if tree.Tree != nil {
			fieldNames(tree.Root, func(name string) {
				if !slices.Contains(out, name) {
					out = append(out, name)
				}
			})
		}
	}

	return out
}

// fieldNames calls use with the first name of every field reference such
// as .NAME below node.
func fieldNames(node parse.Node, use func(string)) { //nolint:cyclop // one case per node type
	switch node := node.(type) {
	case *parse.ListNode:
		if node != nil {
			for _, kid := range node.Nodes {
				fieldNames(kid, use)
			}
		}
	case *parse.ActionNode:
		fieldNames(node.Pipe, use)
	case *parse.PipeNode:
		if node != nil {
			for _, cmd := range node.Cmds {
				fieldNames(cmd, use)
			}
		}
	case *parse.CommandNode:
		for _, arg := range node.Args {
			fieldNames(arg, use)
		}
	case *parse.FieldNode:
		use(node.Ident[0])
	case *parse.VariableNode:
		if node.Ident[0] == "$" && len(node.Ident) > 1 {
			use(node.Ident[1])
		}
	case *parse.ChainNode:
		fieldNames(node.Node, use)
	case *parse.IfNode:
		branchNames(&node.BranchNode, use)
	case *parse.RangeNode:
		branchNames(&node.BranchNode, use)
	case *parse.WithNode:
		branchNames(&node.BranchNode, use)
	case *parse.TemplateNode:
		fieldNames(node.Pipe, use)
	}
}

func branchNames(node *parse.BranchNode, use func(string)) {
	fieldNames(node.Pipe, use)
	fieldNames(node.List, use)
	fieldNames(node.ElseList, use)
}

// notSet is the error for a variable that is not set.
func notSet(name string, opts Options) error {
	if opts.LookupEnv == nil {
		return fmt.Errorf("variable %s is not set; pass --var %s=VALUE, add it to the vars header, or pass --env to read it from the environment", name, name)
	}

	return fmt.Errorf("variable %s is not set; pass --var %s=VALUE, set it in the environment or add it to the vars header", name, name)
}

// quote writes value as a double-quoted YAML string.
func quote(value string) (string, error) {
	// json.Marshal would replace invalid bytes silently.
	if !utf8.ValidString(value) {
		return "", errors.New("quote: the value is not valid UTF-8")
	}

	// A JSON string is a YAML double-quoted scalar; HTML escaping would
	// only make the rendered file harder to read.
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(value); err != nil {
		return "", err //nolint:wrapcheck // encoding a string does not fail
	}

	// JSON leaves characters raw that YAML does not allow in a file.
	var out strings.Builder

	for _, r := range strings.TrimSuffix(buf.String(), "\n") {
		if unicode.Is(yamlPrintable, r) {
			out.WriteRune(r)
		} else {
			fmt.Fprintf(&out, `\u%04X`, r)
		}
	}

	return out.String(), nil
}

// yamlPrintable holds the characters YAML allows unescaped in a file
// (YAML 1.2, production c-printable), except U+0085, which a double-quoted
// string folds as a line break.
var yamlPrintable = &unicode.RangeTable{ //nolint:gochecknoglobals // lookup table
	R16: []unicode.Range16{
		{Lo: 0x09, Hi: 0x0A, Stride: 1}, {Lo: 0x0D, Hi: 0x0D, Stride: 1},
		{Lo: 0x20, Hi: 0x7E, Stride: 1},
		{Lo: 0xA0, Hi: 0xD7FF, Stride: 1}, {Lo: 0xE000, Hi: 0xFFFD, Stride: 1},
	},
	R32: []unicode.Range32{{Lo: 0x10000, Hi: 0x10FFFF, Stride: 1}},
}

// templateLine matches a line that text/template names inside a message.
var templateLine = regexp.MustCompile(`(started at )msi\.yaml:(\d+)`)

// missingKey matches text/template's error for a variable that is not set.
var missingKey = regexp.MustCompile(`map has no entry for key "([^"]*)"`)

// templateErr drops the template name that text/template puts in every
// message, which Load already names, and says how to set a missing
// variable, so an error reads "template: line 3:5: at <.ARCH>: variable
// ARCH is not set; ...".
func templateErr(err error, opts Options, offset int) error {
	msg := strings.TrimPrefix(err.Error(), "template: ")
	msg = strings.TrimPrefix(msg, "msi.yaml:")
	msg = strings.Replace(msg, `executing "msi.yaml" at `, "at ", 1)
	msg = templateLine.ReplaceAllStringFunc(msg, func(m string) string {
		sub := templateLine.FindStringSubmatch(m)
		n, _ := strconv.Atoi(sub[2])

		return sub[1] + strconv.Itoa(n+offset)
	})

	if m := missingKey.FindStringSubmatch(msg); m != nil {
		msg = strings.Replace(msg, m[0], notSet(m[1], opts).Error(), 1)
	}

	if line, rest, ok := strings.Cut(msg, ":"); ok && line != "" && strings.Trim(line, "0123456789") == "" {
		n, _ := strconv.Atoi(line)

		return fmt.Errorf("line %d:%s", n+offset, rest)
	}

	return errors.New(msg)
}

// limitedBuffer is a bytes.Buffer that refuses to grow past left bytes.
type limitedBuffer struct {
	bytes.Buffer

	left int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if len(data) > b.left {
		b.left = -1

		return 0, errRenderedTooLarge
	}

	b.left -= len(data)

	return b.Buffer.Write(data) //nolint:wrapcheck // bytes.Buffer does not fail
}
