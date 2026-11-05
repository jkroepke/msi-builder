// Package condition checks Windows Installer conditional statements against
// the grammar of docs/spec/MSISPEC.md section 8.8. The engine ignores a
// LaunchCondition or action condition that does not parse, so a builder has
// to reject it (UPG-02, section 9.1). The package parses only; it does not
// evaluate.
package condition

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Error is a syntax error with its position.
type Error struct {
	Msg string
	Pos int
}

func (e *Error) Error() string {
	return fmt.Sprintf("condition: %s at offset %d", e.Msg, e.Pos)
}

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokIdent
	tokNumber
	tokString
	tokLParen
	tokRParen
	tokOp      // comparison operator, possibly with a ~ prefix
	tokKeyword // AND OR NOT XOR EQV IMP
	tokPrefix  // one of % $ ? & ! followed by an identifier
)

type token struct {
	text string
	kind tokenKind
	pos  int
}

var keywords = map[string]bool{"AND": true, "OR": true, "NOT": true, "XOR": true, "EQV": true, "IMP": true} //nolint:gochecknoglobals // lookup table

func isIdentStart(r rune) bool { return r == '_' || unicode.IsLetter(r) }

func isIdentPart(r rune) bool {
	return r == '_' || r == '.' || r == '#' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

type lexer struct {
	s   string
	pos int
}

func (l *lexer) next() (token, error) { //nolint:cyclop,gocyclo,gocognit // one pass over the format; splitting would scatter it
	for l.pos < len(l.s) && (l.s[l.pos] == ' ' || l.s[l.pos] == '\t' || l.s[l.pos] == '\r' || l.s[l.pos] == '\n') {
		l.pos++
	}

	if l.pos >= len(l.s) {
		return token{kind: tokEOF, pos: l.pos}, nil
	}

	start := l.pos
	char := l.s[l.pos]

	switch {
	case char == '(':
		l.pos++

		return token{kind: tokLParen, text: "(", pos: start}, nil
	case char == ')':
		l.pos++

		return token{kind: tokRParen, text: ")", pos: start}, nil
	case char == '"':
		end := strings.IndexByte(l.s[l.pos+1:], '"')
		if end < 0 {
			return token{}, &Error{Pos: start, Msg: "unterminated string"}
		}

		l.pos += end + 2

		return token{kind: tokString, text: l.s[start:l.pos], pos: start}, nil
	case char == '%' || char == '$' || char == '?' || char == '&' || char == '!':
		l.pos++

		if l.pos >= len(l.s) || !isIdentStart(rune(l.s[l.pos])) {
			return token{}, &Error{Pos: start, Msg: fmt.Sprintf("%q must be followed by a name", char)}
		}

		for l.pos < len(l.s) && isIdentPart(rune(l.s[l.pos])) {
			l.pos++
		}

		return token{kind: tokPrefix, text: l.s[start:l.pos], pos: start}, nil
	case char == '~' || char == '=' || char == '<' || char == '>':
		mark := l.pos
		if char == '~' {
			mark++
		}

		if mark < len(l.s) {
			switch l.s[mark] {
			case '=':
				l.pos = mark + 1

				return token{kind: tokOp, text: l.s[start:l.pos], pos: start}, nil
			case '<':
				if mark+1 < len(l.s) && (l.s[mark+1] == '>' || l.s[mark+1] == '=' || l.s[mark+1] == '<') {
					l.pos = mark + 2
				} else {
					l.pos = mark + 1
				}

				return token{kind: tokOp, text: l.s[start:l.pos], pos: start}, nil
			case '>':
				if mark+1 < len(l.s) && (l.s[mark+1] == '=' || l.s[mark+1] == '<' || l.s[mark+1] == '>') {
					l.pos = mark + 2
				} else {
					l.pos = mark + 1
				}

				return token{kind: tokOp, text: l.s[start:l.pos], pos: start}, nil
			}
		}

		return token{}, &Error{Pos: start, Msg: "'~' must be followed by a comparison operator"}
	case char == '-' || (char >= '0' && char <= '9'):
		l.pos++
		for l.pos < len(l.s) && (l.s[l.pos] >= '0' && l.s[l.pos] <= '9' || l.s[l.pos] == '.') {
			l.pos++
		}

		if l.s[start:l.pos] == "-" {
			return token{}, &Error{Pos: start, Msg: "'-' is not a number"}
		}

		return token{kind: tokNumber, text: l.s[start:l.pos], pos: start}, nil
	}

	first, _ := utf8.DecodeRuneInString(l.s[l.pos:])
	if isIdentStart(first) {
		for l.pos < len(l.s) {
			r, size := utf8.DecodeRuneInString(l.s[l.pos:])
			if !isIdentPart(r) {
				break
			}

			l.pos += size
		}

		text := l.s[start:l.pos]
		if keywords[strings.ToUpper(text)] {
			return token{kind: tokKeyword, text: strings.ToUpper(text), pos: start}, nil
		}

		return token{kind: tokIdent, text: text, pos: start}, nil
	}

	return token{}, &Error{Pos: start, Msg: fmt.Sprintf("unexpected character %q", first)}
}

type parser struct {
	lex *lexer
	tok token
}

func (p *parser) advance() error {
	t, err := p.lex.next()
	if err != nil {
		return err
	}

	p.tok = t

	return nil
}

// Check reports whether s is a syntactically valid condition. An empty or
// blank condition is valid (it means "no condition").
func Check(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}

	parse := &parser{lex: &lexer{s: s}}
	if err := parse.advance(); err != nil {
		return err
	}

	if err := parse.expr(); err != nil {
		return err
	}

	if parse.tok.kind != tokEOF {
		return &Error{Pos: parse.tok.pos, Msg: fmt.Sprintf("unexpected %q", parse.tok.text)}
	}

	return nil
}

// binary parses a left-associative chain of the keyword over the next level.
func (p *parser) binary(keyword string, next func() error) error {
	if err := next(); err != nil {
		return err
	}

	for p.tok.kind == tokKeyword && p.tok.text == keyword {
		if err := p.advance(); err != nil {
			return err
		}

		if err := next(); err != nil {
			return err
		}
	}

	return nil
}

func (p *parser) expr() error { return p.binary("IMP", p.eqv) }
func (p *parser) eqv() error  { return p.binary("EQV", p.xor) }
func (p *parser) xor() error  { return p.binary("XOR", p.or) }
func (p *parser) or() error   { return p.binary("OR", p.and) }
func (p *parser) and() error  { return p.binary("AND", p.not) }

func (p *parser) not() error {
	if p.tok.kind == tokKeyword && p.tok.text == "NOT" {
		if err := p.advance(); err != nil {
			return err
		}

		return p.not()
	}

	return p.term()
}

func (p *parser) term() error {
	if p.tok.kind == tokLParen {
		if err := p.advance(); err != nil {
			return err
		}

		if err := p.expr(); err != nil {
			return err
		}

		if p.tok.kind != tokRParen {
			return &Error{Pos: p.tok.pos, Msg: "missing ')'"}
		}

		return p.advance()
	}

	if err := p.value(); err != nil {
		return err
	}

	if p.tok.kind == tokOp {
		if err := p.advance(); err != nil {
			return err
		}

		return p.value()
	}

	return nil
}

func (p *parser) value() error {
	switch p.tok.kind { //nolint:exhaustive // the default covers the remaining kinds
	case tokIdent, tokNumber, tokString, tokPrefix:
		return p.advance()
	case tokEOF:
		return &Error{Pos: p.tok.pos, Msg: "unexpected end of condition"}
	case tokKeyword:
		return &Error{Pos: p.tok.pos, Msg: "unexpected keyword " + p.tok.text}
	default:
		return &Error{Pos: p.tok.pos, Msg: fmt.Sprintf("unexpected %q", p.tok.text)}
	}
}

// Properties returns the property names a condition refers to, without the
// feature, component and environment references.
func Properties(s string) []string {
	l := &lexer{s: s}

	var out []string

	for {
		tok, err := l.next()
		if err != nil || tok.kind == tokEOF {
			return out
		}

		if tok.kind == tokIdent {
			out = append(out, tok.text)
		}
	}
}

// ActionStates returns the feature and component action state references of
// a condition (&Feature, $Component), in order.
func ActionStates(s string) []string { return prefixed(s, "&$") }

// States returns the feature and component state references of a
// condition, installed (!Feature, ?Component) and action (&Feature,
// $Component), in order.
func States(s string) []string { return prefixed(s, "!?&$") }

// prefixed returns the references of a condition whose prefix is one of
// prefixes, in order.
func prefixed(s, prefixes string) []string {
	l := &lexer{s: s}

	var out []string

	for {
		tok, err := l.next()
		if err != nil || tok.kind == tokEOF {
			return out
		}

		if tok.kind == tokPrefix && strings.IndexByte(prefixes, tok.text[0]) >= 0 {
			out = append(out, tok.text)
		}
	}
}

// RenameProperties returns s with each property name that rename maps
// replaced, leaving strings, feature, component and environment references
// and everything else as it is. A byte the grammar does not read is kept
// and lexing goes on after it, so a caller that passes text holding other
// syntax, such as template actions, must split it off first.
func RenameProperties(cond string, rename func(name string) (string, bool)) string {
	lex := &lexer{s: cond}

	var out strings.Builder

	last := 0

	for {
		tok, err := lex.next()

		var syntax *Error

		switch {
		case errors.As(err, &syntax):
			lex.pos = syntax.Pos + 1

			continue
		case err != nil || tok.kind == tokEOF:
			out.WriteString(cond[last:])

			return out.String()
		case tok.kind != tokIdent:
			continue
		}

		if to, ok := rename(tok.text); ok {
			out.WriteString(cond[last:tok.pos])
			out.WriteString(to)

			last = tok.pos + len(tok.text)
		}
	}
}
