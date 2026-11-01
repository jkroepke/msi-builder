package wixconvert

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ppToken is a token of an <?if?> expression.
type ppToken struct {
	text string
	kind ppKind
}

type ppKind int

const (
	ppWord ppKind = iota
	ppString
	ppVar
	ppOp
	ppOpen
	ppClose
)

var errPPSyntax = errors.New("not an expression convert can read")

// ppTokens splits an <?if?> expression.
func ppTokens(text string) ([]ppToken, error) { //nolint:cyclop // one branch per kind of token
	var tokens []ppToken

	for i := 0; i < len(text); {
		switch char := text[i]; {
		case char == ' ' || char == '\t' || char == '\r' || char == '\n':
			i++
		case char == '(' || char == ')':
			kind := ppOpen
			if char == ')' {
				kind = ppClose
			}

			tokens = append(tokens, ppToken{text: text[i : i+1], kind: kind})
			i++
		case char == '$' && strings.HasPrefix(text[i:], "$("):
			end := strings.IndexByte(text[i:], ')')
			if end < 0 {
				return nil, errPPSyntax
			}

			tokens = append(tokens, ppToken{text: text[i : i+end+1], kind: ppVar})
			i += end + 1
		case char == '"' || char == '\'':
			end := strings.IndexByte(text[i+1:], char)
			if end < 0 {
				return nil, errPPSyntax
			}

			tokens = append(tokens, ppToken{text: text[i+1 : i+1+end], kind: ppString})
			i += end + 2
		case strings.ContainsRune("=!~<>", rune(char)):
			oper := text[i : i+1]
			if i+1 < len(text) && text[i+1] == '=' {
				oper = text[i : i+2]
			}

			if oper == "!" || oper == "~" {
				return nil, errPPSyntax
			}

			tokens = append(tokens, ppToken{text: oper, kind: ppOp})
			i += len(oper)
		default:
			end := i
			for end < len(text) && !strings.ContainsRune(" \t\r\n()=!~<>\"'", rune(text[end])) {
				end++
			}

			tokens = append(tokens, ppToken{text: text[i:end], kind: ppWord})
			i = end
		}
	}

	return tokens, nil
}

// ppParser turns an <?if?> expression into a template pipeline.
type ppParser struct {
	conv   *converter
	tokens []ppToken
	loops  []string
}

// ppCondition turns the expression of an <?if?> or <?elseif?> into the
// condition of a template if action. Comparisons are case-sensitive except
// ~=; < and > compare numbers when one side is a number.
func (c *converter) ppCondition(text string, loops []string) (string, error) {
	tokens, err := ppTokens(text)
	if err != nil {
		return "", fmt.Errorf("%q: %w", text, err)
	}

	parser := &ppParser{conv: c, tokens: tokens, loops: loops}

	expr, err := parser.or()
	if err != nil {
		return "", fmt.Errorf("%q: %w", text, err)
	}

	if len(parser.tokens) > 0 {
		return "", fmt.Errorf("%q: %w", text, errPPSyntax)
	}

	return expr, nil
}

func (p *ppParser) peekKeyword(word string) bool {
	return len(p.tokens) > 0 && p.tokens[0].kind == ppWord && strings.EqualFold(p.tokens[0].text, word)
}

func (p *ppParser) or() (string, error) {
	return p.chain("or", p.and)
}

func (p *ppParser) and() (string, error) {
	return p.chain("and", p.unary)
}

// chain reads operands joined by a keyword.
func (p *ppParser) chain(keyword string, next func() (string, error)) (string, error) {
	first, err := next()
	if err != nil {
		return "", err
	}

	operands := []string{first}

	for p.peekKeyword(keyword) {
		p.tokens = p.tokens[1:]

		operand, err := next()
		if err != nil {
			return "", err
		}

		operands = append(operands, operand)
	}

	if len(operands) == 1 {
		return first, nil
	}

	for i, operand := range operands {
		operands[i] = paren(operand)
	}

	return keyword + " " + strings.Join(operands, " "), nil
}

func (p *ppParser) unary() (string, error) {
	if p.peekKeyword("not") {
		p.tokens = p.tokens[1:]

		operand, err := p.unary()
		if err != nil {
			return "", err
		}

		return "not " + paren(operand), nil
	}

	return p.primary()
}

func (p *ppParser) primary() (string, error) {
	if len(p.tokens) == 0 {
		return "", errPPSyntax
	}

	if p.tokens[0].kind != ppOpen {
		return p.comparison()
	}

	p.tokens = p.tokens[1:]

	expr, err := p.or()
	if err != nil {
		return "", err
	}

	if len(p.tokens) == 0 || p.tokens[0].kind != ppClose {
		return "", errPPSyntax
	}

	p.tokens = p.tokens[1:]

	return expr, nil
}

// comparison reads an operand and the comparison that may follow it.
func (p *ppParser) comparison() (string, error) {
	left, err := p.operand()
	if err != nil {
		return "", err
	}

	if len(p.tokens) == 0 || p.tokens[0].kind != ppOp {
		if left.kind != ppVar {
			return "", errPPSyntax
		}

		// A variable on its own tests that it is not empty.
		return left.text, nil
	}

	oper := p.tokens[0].text
	p.tokens = p.tokens[1:]

	right, err := p.operand()
	if err != nil {
		return "", err
	}

	return compare(oper, left, right), nil
}

// ppOperandTok is a converted operand and the kind of token it came from.
type ppOperandTok struct {
	text string
	kind ppKind
	// number is set on a literal that is a whole number.
	number bool
}

func (p *ppParser) operand() (ppOperandTok, error) {
	if len(p.tokens) == 0 {
		return ppOperandTok{}, errPPSyntax
	}

	tok := p.tokens[0]
	p.tokens = p.tokens[1:]

	switch tok.kind {
	case ppVar:
		m := preprocessorVar.FindStringSubmatch(tok.text)
		if m == nil || m[0] != tok.text || m[1] == varSys {
			return ppOperandTok{}, errPPSyntax
		}

		return ppOperandTok{text: p.conv.varOperand(m[2], p.loops), kind: ppVar}, nil
	case ppWord, ppString:
		return literalOperand(tok)
	case ppOp, ppOpen, ppClose:
	}

	return ppOperandTok{}, errPPSyntax
}

// literalOperand converts a word or quoted string.
func literalOperand(tok ppToken) (ppOperandTok, error) {
	if tok.kind == ppWord && isKeyword(tok.text) || strings.Contains(tok.text, "$(") {
		return ppOperandTok{}, errPPSyntax
	}

	_, err := strconv.Atoi(tok.text)

	return ppOperandTok{text: strconv.Quote(tok.text), kind: tok.kind, number: err == nil}, nil
}

// isKeyword reports whether a word is And, Or or Not.
func isKeyword(word string) bool {
	return strings.EqualFold(word, "and") || strings.EqualFold(word, "or") || strings.EqualFold(word, "not")
}

// compare writes a comparison as a template pipeline.
func compare(oper string, left, right ppOperandTok) string {
	switch oper {
	case "=":
		return "eq " + left.text + " " + right.text
	case "!=":
		return "ne " + left.text + " " + right.text
	case "~=":
		return "eq (lower " + left.text + ") (lower " + right.text + ")"
	}

	fn := map[string]string{"<": "lt", "<=": "le", ">": "gt", ">=": "ge"}[oper]
	if left.number || right.number {
		return fn + " " + number(left) + " " + number(right)
	}

	return fn + " " + left.text + " " + right.text
}

// number returns an operand as a whole number.
func number(operand ppOperandTok) string {
	if operand.number {
		return strings.Trim(operand.text, `"`)
	}

	return "(atoi " + operand.text + ")"
}
