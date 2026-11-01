package wixconvert

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/text/encoding/htmlindex"
)

// Namespaces of the WiX schemas, by the short name the converter uses.
// The v3 URIs and the v4 and later ones mean the same elements.
var namespaces = map[string]string{ //nolint:gochecknoglobals // lookup table
	"":                                     nsCore,
	"http://wixtoolset.org/schemas/v4/wxs": nsCore,
	"http://schemas.microsoft.com/wix/2006/wi":             nsCore,
	"http://wixtoolset.org/schemas/v4/wxs/util":            nsUtil,
	"http://schemas.microsoft.com/wix/UtilExtension":       nsUtil,
	"http://wixtoolset.org/schemas/v4/wxs/firewall":        nsFirewall,
	"http://schemas.microsoft.com/wix/FirewallExtension":   nsFirewall,
	"http://wixtoolset.org/schemas/v4/wxs/ui":              nsUI,
	"http://wixtoolset.org/schemas/v4/wxs/netfx":           nsOther,
	"http://schemas.microsoft.com/wix/NetFxExtension":      nsOther,
	"http://wixtoolset.org/schemas/v4/wxs/difxapp":         nsOther,
	"http://schemas.microsoft.com/wix/DifxAppExtension":    nsOther,
	"http://wixtoolset.org/schemas/v4/wxs/iis":             nsOther,
	"http://schemas.microsoft.com/wix/IIsExtension":        nsOther,
	"http://wixtoolset.org/schemas/v4/wxs/sql":             nsOther,
	"http://schemas.microsoft.com/wix/SqlExtension":        nsOther,
	"http://wixtoolset.org/schemas/v4/wxs/http":            nsOther,
	"http://wixtoolset.org/schemas/v4/wxs/dependency":      nsOther,
	"http://schemas.microsoft.com/wix/DependencyExtension": nsOther,
}

// Short namespace names.
const (
	nsCore     = ""
	nsUtil     = "util"
	nsFirewall = "fw"
	nsUI       = "ui"
	nsOther    = "ext"
)

// elem is an element of the source file.
type elem struct {
	attrs    map[string]string
	ns       string
	space    string
	name     string
	text     string
	order    []string
	kids     []*elem
	comments []string
	// pos are the preprocessor blocks and branches the element sits in.
	pos []ppPos
	// opens are the template actions the element's output sits in: those
	// of the blocks in pos that convert writes as templates.
	opens []openRef
	// loops are the loop variables of the <?foreach?> blocks in opens.
	loops []string
	line  int
}

// qname is the element's name with its namespace prefix, as a WiX author
// would write it.
func (e *elem) qname() string {
	switch {
	case e.ns == nsCore:
		return e.name
	case e.ns == nsOther && e.space != "":
		return e.space + ":" + e.name
	default:
		return e.ns + ":" + e.name
	}
}

func (e *elem) attr(name string) string { return e.attrs[name] }

func (e *elem) has(name string) bool {
	_, ok := e.attrs[name]

	return ok
}

// yes reports whether a WiX yes/no attribute is yes.
func (e *elem) yes(name string) bool {
	switch strings.ToLower(e.attrs[name]) {
	case "yes", "true", "1":
		return true
	}

	return false
}

// no reports whether a WiX yes/no attribute is explicitly no.
func (e *elem) no(name string) bool {
	switch strings.ToLower(e.attrs[name]) {
	case "no", "false", "0":
		return true
	}

	return false
}

// is reports whether the element is the core element name.
func (e *elem) is(name string) bool { return e.ns == nsCore && e.name == name }

// procInst is a preprocessor instruction such as <?define?> or <?if?>.
type procInst struct {
	// block is the block an if, elseif, else, endif, foreach or
	// endforeach belongs to.
	block  *ppBlock
	target string
	inst   string
	// text is inst as the source writes it, for notes: fillRedefined
	// fills values into inst.
	text string
	// pos are the blocks the instruction sits in; for one that opens,
	// continues or closes a block, the blocks around that block.
	pos  []ppPos
	line int
	// skipped is set on an instruction inside a left-out branch.
	skipped bool
	// leftOut is set on an elseif or else whose branch is left out.
	leftOut bool
}

// document is a parsed source file.
type document struct {
	root  *elem
	procs []procInst
	// extAttrs are the extension attributes on WiX elements, which the
	// converter does not read.
	extAttrs []extAttr
	// blocks are the <?if?> and <?foreach?> blocks in source order.
	blocks []*ppBlock
	// mixed is set when an element or text straddles the branches of a
	// block, which only the first-branch reading can convert.
	mixed bool
}

// extAttr is an extension attribute, such as util:Foo on a File.
type extAttr struct {
	e    *elem
	elem string
	name string
	line int
}

// parse reads a .wxs file into an element tree. Preprocessor instructions
// are kept aside. Every branch of a block is read, so that the converter
// can write the blocks as templates; when an element or text straddles
// branches, only the first branch of each block is read.
func parse(r io.Reader) (*document, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	data, err = toUTF8(data)
	if err != nil {
		return nil, err
	}

	// A file that is only well-formed one branch at a time, such as one
	// with a root element per branch, is read by its first branches.
	doc, err := parseData(data, false)
	if err == nil && !doc.mixed {
		return doc, nil
	}

	return parseData(data, true)
}

// parseData builds the tree; firstBranch reads only the first branch of
// each block.
func parseData(data []byte, firstBranch bool) (*document, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	// toUTF8 has decoded the file already.
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	tree := &treeBuilder{doc: &document{}, firstBranch: firstBranch}

	// line counts the newlines up to the token, without rescanning the
	// file for every token.
	line, counted := 1, int64(0)

	for {
		offset := dec.InputOffset()
		line += bytes.Count(data[counted:offset], []byte("\n"))
		counted = offset

		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("parse: %w", err)
		}

		if err := tree.token(tok, line); err != nil {
			return nil, err
		}
	}

	if tree.doc.root == nil {
		return nil, errors.New("parse: no root element")
	}

	return tree.doc, nil
}

// xmlEncoding matches the encoding of an XML declaration.
var xmlEncoding = regexp.MustCompile(`^\s*<\?xml[^>]*\sencoding\s*=\s*["']([^"']+)["']`)

// toUTF8 decodes a file whose XML declaration names another encoding, such
// as windows-1252, which WiX sources often declare. The whole file is
// decoded up front so that line numbers count the decoded text.
func toUTF8(data []byte) ([]byte, error) {
	match := xmlEncoding.FindSubmatch(data)
	if match == nil || strings.EqualFold(string(match[1]), "utf-8") {
		return data, nil
	}

	enc, err := htmlindex.Get(string(match[1]))
	if err != nil {
		return nil, fmt.Errorf("parse: encoding %q: %w", match[1], err)
	}

	decoded, err := enc.NewDecoder().Bytes(data)
	if err != nil {
		return nil, fmt.Errorf("parse: encoding %q: %w", match[1], err)
	}

	return decoded, nil
}

// treeBuilder builds the element tree from the tokens of a file.
type treeBuilder struct {
	doc      *document
	stack    []*elem
	comments []string
	// pos are the open blocks and the branch read in each.
	pos []ppPos
	// branches has one entry per open <?if?>: whether the branch being
	// read is one the converter leaves out.
	branches []bool
	// skipDepth counts the open elements of a left-out branch.
	skipDepth int
	// firstBranch reads only the first branch of each block.
	firstBranch bool
}

// skipping reports whether the tokens belong to a left-out branch.
func (b *treeBuilder) skipping() bool {
	return slices.Contains(b.branches, true)
}

// procInst records a processing instruction. Reading only the first
// branch of an <?if?>, the other branches are left out: converting every
// branch makes duplicate IDs, and the first branch is usually the default
// build.
func (b *treeBuilder) procInst(tok xml.ProcInst, line int) {
	if tok.Target == "xml" {
		return
	}

	inst := strings.TrimSpace(string(tok.Inst))

	proc := procInst{target: tok.Target, inst: inst, text: inst, line: line}
	if b.firstBranch {
		b.firstBranchInst(&proc)
	}

	b.track(&proc)
	b.doc.procs = append(b.doc.procs, proc)
}

// firstBranchInst marks the instructions of left-out branches.
func (b *treeBuilder) firstBranchInst(proc *procInst) {
	switch proc.target {
	case piIf, piIfdef, piIfndef:
		proc.skipped = b.skipping()
		b.branches = append(b.branches, false)
	case piElseif, piElse:
		if len(b.branches) > 0 {
			outer := slices.Contains(b.branches[:len(b.branches)-1], true)
			proc.skipped, proc.leftOut = outer, !outer
			b.branches[len(b.branches)-1] = true
		}
	case piEndif:
		if len(b.branches) > 0 {
			b.branches = b.branches[:len(b.branches)-1]
		}
	default:
		proc.skipped = b.skipping()
	}
}

// track follows the blocks and branches the tokens sit in.
func (b *treeBuilder) track(proc *procInst) {
	proc.pos = slices.Clone(b.pos)

	var top *ppBlock
	if len(b.pos) > 0 {
		top = b.pos[len(b.pos)-1].block
	}

	switch proc.target {
	case piIf, piIfdef, piIfndef, piForeach:
		blk := &ppBlock{pos: proc.pos, line: proc.line, keep: !b.firstBranch, foreach: proc.target == piForeach}
		blk.branches = append(blk.branches, ppBranch{target: proc.target, inst: proc.inst, line: proc.line})
		proc.block = blk
		b.doc.blocks = append(b.doc.blocks, blk)
		b.pos = append(b.pos, ppPos{block: blk})
	case piElseif, piElse:
		if top != nil && !top.foreach {
			top.branches = append(top.branches, ppBranch{target: proc.target, inst: proc.inst, line: proc.line})
			b.pos[len(b.pos)-1].branch++
			proc.block, proc.pos = top, slices.Clone(b.pos[:len(b.pos)-1])
		}
	case piEndif, piEndforeach:
		if top != nil && top.foreach == (proc.target == piEndforeach) {
			b.pos = b.pos[:len(b.pos)-1]
			proc.block, proc.pos = top, slices.Clone(b.pos)
		}
	}
}

func (b *treeBuilder) token(tok xml.Token, line int) error { //nolint:cyclop // one branch per token type
	if b.skip(tok) {
		return nil
	}

	switch tok := tok.(type) {
	case xml.StartElement:
		elm := newElem(tok, line, b.comments)
		elm.pos = slices.Clone(b.pos)

		for _, attr := range tok.Attr {
			if attr.Name.Space != "" && attr.Name.Space != "xmlns" && !strings.HasPrefix(attr.Name.Space, "http://www.w3.org/") {
				name := prefix(attr.Name.Space) + ":" + attr.Name.Local
				b.doc.extAttrs = append(b.doc.extAttrs, extAttr{e: elm, elem: elm.qname(), name: name, line: line})
			}
		}

		return b.start(elm)
	case xml.EndElement:
		if !b.firstBranch && !slices.Equal(b.stack[len(b.stack)-1].pos, b.pos) {
			b.doc.mixed = true
		}

		b.stack = b.stack[:len(b.stack)-1]
		b.comments = nil
	case xml.CharData:
		if len(b.stack) > 0 {
			top := b.stack[len(b.stack)-1]
			if !b.firstBranch && strings.TrimSpace(string(tok)) != "" && !slices.Equal(top.pos, b.pos) {
				b.doc.mixed = true
			}

			top.text += string(tok)
		}
	case xml.Comment:
		b.comments = append(b.comments, string(tok))
	case xml.ProcInst:
		b.procInst(tok, line)
	}

	return nil
}

// skip drops a token of a left-out branch and reports whether it did. The
// end of an element that began before the branch is kept.
func (b *treeBuilder) skip(tok xml.Token) bool {
	if !b.firstBranch || b.skipDepth == 0 && !b.skipping() {
		return false
	}

	switch tok.(type) {
	case xml.ProcInst:
		return false
	case xml.StartElement:
		b.skipDepth++
	case xml.EndElement:
		if b.skipDepth == 0 {
			return false
		}

		b.skipDepth--
	}

	b.comments = nil

	return true
}

func (b *treeBuilder) start(elm *elem) error {
	b.comments = nil

	switch {
	case len(b.stack) > 0:
		parent := b.stack[len(b.stack)-1]
		parent.kids = append(parent.kids, elm)
	case b.doc.root != nil:
		return errors.New("parse: more than one root element")
	default:
		b.doc.root = elm
	}

	b.stack = append(b.stack, elm)

	return nil
}

func newElem(tok xml.StartElement, line int, comments []string) *elem {
	short, known := namespaces[tok.Name.Space]
	if !known {
		short = nsOther
	}

	elm := &elem{
		ns:       short,
		space:    tok.Name.Space,
		name:     tok.Name.Local,
		attrs:    make(map[string]string, len(tok.Attr)),
		line:     line,
		comments: comments,
	}

	if !known {
		elm.space = shortSpace(tok.Name.Space)
	}

	for _, attr := range tok.Attr {
		// Namespace declarations and the attributes of extensions
		// (util:..., bal:...) are not converted.
		if attr.Name.Space != "" || attr.Name.Local == "xmlns" {
			continue
		}

		elm.attrs[attr.Name.Local] = attr.Value
		elm.order = append(elm.order, attr.Name.Local)
	}

	return elm
}

// prefix names a namespace as a WiX author writes it, such as util.
func prefix(uri string) string {
	if short, ok := namespaces[uri]; ok && short != nsOther {
		return short
	}

	return shortSpace(uri)
}

// shortSpace names an unknown namespace by its last path segment.
func shortSpace(uri string) string {
	uri = strings.TrimRight(uri, "/")
	if i := strings.LastIndexAny(uri, "/:"); i >= 0 {
		return uri[i+1:]
	}

	return uri
}
