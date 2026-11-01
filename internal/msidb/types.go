// Package msidb models a Windows Installer database and serializes it into
// the streams of a compound file: string pool, catalog, column-major table
// streams, binary streams, embedded streams and summary information
// (docs/spec/MSISPEC.md section 3). Unmarshal reads a package back, which the
// builder uses to verify its own output.
package msidb

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/jkroepke/msi-builder/internal/cfb"
	"github.com/jkroepke/msi-builder/internal/summary"
)

// Kind is a column's storage kind.
type Kind int

// Column kinds.
const (
	KindString Kind = iota
	KindInt16
	KindInt32
	KindBinary
)

// Limits of the engine (MSIDB-07, MSIDB-11, section 3).
const (
	MaxColumns      = 31
	MaxTableName    = 31
	MaxRefcount     = 32767 // msi.dll keeps 15 bits (MSIDB-05)
	longRefThresh   = 65535 // highest ID at which references become 3 bytes (MSIDB-06)
	MinInt16        = -32767
	MaxInt16        = 32767
	MinInt32        = -2147483647
	MaxInt32        = 2147483647
	maxColumnName   = 64
	maxStringLength = 0xFFFFFFFF
)

// Type is a column type as the .idt notation describes it: s72, S255, l0,
// L255, i2, I4, v0, V0. Key marks a primary key column.
type Type struct {
	Kind        Kind
	Nullable    bool
	Localizable bool
	Key         bool
	// Width is the maximum string length in characters (0 = unbounded); it
	// is 2 or 4 for integers and 0 for binary.
	Width int
}

var typePattern = regexp.MustCompile(`^([sSlL])(\d{1,3})$|^([iI])([24])$|^([vV])0$`)

// ParseType parses the .idt notation. A trailing "!" or the Key field marks
// a primary key column in callers; ParseType itself only reads the type.
func ParseType(s string) (Type, error) {
	match := typePattern.FindStringSubmatch(s)
	if match == nil {
		return Type{}, fmt.Errorf("msidb: %q is not a column type (s72, S255, l0, L255, i2, I4, v0, V0)", s)
	}

	switch {
	case match[1] != "":
		width, _ := strconv.Atoi(match[2])
		if width > 255 {
			return Type{}, fmt.Errorf("msidb: string width %d exceeds 255", width)
		}

		return Type{
			Kind:        KindString,
			Nullable:    match[1] == "S" || match[1] == "L",
			Localizable: match[1] == "l" || match[1] == "L",
			Width:       width,
		}, nil
	case match[3] != "":
		t := Type{Kind: KindInt16, Width: 2, Nullable: match[3] == "I"}
		if match[4] == "4" {
			t.Kind, t.Width = KindInt32, 4
		}

		return t, nil
	default:
		return Type{Kind: KindBinary, Nullable: match[5] == "V"}, nil
	}
}

// MustType is ParseType for literals in table definitions; it panics on a
// malformed type.
func MustType(s string) Type {
	t, err := ParseType(s)
	if err != nil {
		panic(err)
	}

	return t
}

// String returns the .idt notation.
func (t Type) String() string {
	switch t.Kind { //nolint:exhaustive // the default covers the remaining kinds
	case KindString:
		code := "s"
		if t.Localizable {
			code = "l"
		}

		if t.Nullable {
			code = strings.ToUpper(code)
		}

		return code + strconv.Itoa(t.Width)
	case KindInt16:
		if t.Nullable {
			return "I2"
		}

		return "i2"
	case KindInt32:
		if t.Nullable {
			return "I4"
		}

		return "i4"
	default:
		if t.Nullable {
			return "V0"
		}

		return "v0"
	}
}

// Word is the 16-bit type word stored in _Columns.Type (section 3).
func (t Type) Word() uint16 {
	var word uint16

	switch t.Kind {
	case KindString:
		word = 0x0D00 | uint16(t.Width) //nolint:gosec // bounded by the format limits checked earlier
		if t.Localizable {
			word |= 0x0200
		}
	case KindInt16:
		word = 0x0502
	case KindInt32:
		word = 0x0104
	case KindBinary:
		word = 0x0900
	}

	if t.Nullable {
		word |= 0x1000
	}

	if t.Key {
		word |= 0x2000
	}

	return word
}

// TypeFromWord decodes a _Columns.Type word.
func TypeFromWord(word uint16) (Type, error) {
	typ := Type{Nullable: word&0x1000 != 0, Key: word&0x2000 != 0}

	switch {
	case word&^0x3000 == 0x0900:
		typ.Kind = KindBinary
	case word&0x0800 != 0:
		typ.Kind = KindString
		typ.Width = int(word & 0xFF)
		typ.Localizable = word&0x0200 != 0
	case word&0xFF == 4:
		typ.Kind, typ.Width = KindInt32, 4
	case word&0xFF == 1 || word&0xFF == 2:
		typ.Kind, typ.Width = KindInt16, 2
	default:
		return Type{}, fmt.Errorf("msidb: unknown column type word %#x", word)
	}

	return typ, nil
}

// nameUnits is the length of a name in UTF-16 units.
func nameUnits(name string) int {
	return len(utf16.Encode([]rune(name)))
}

// check refuses a type that ParseType would not return, such as one built
// directly with a string width above 255, which the type word cannot hold.
func (t Type) check() error {
	canon, err := ParseType(t.String())
	if err != nil {
		return err
	}

	canon.Key = t.Key

	if canon != t {
		return fmt.Errorf("type %+v is not a valid column type (the nearest is %s)", t, canon)
	}

	return nil
}

// storageWidth is the number of bytes a cell takes in a table stream.
func (t Type) storageWidth(longRefs bool) int {
	switch t.Kind { //nolint:exhaustive // the default covers the remaining kinds
	case KindInt32:
		return 4
	case KindString:
		if longRefs {
			return 3
		}

		return 2
	default:
		return 2
	}
}

// Column is a table column.
type Column struct {
	Name string
	Type Type
}

// Col is a shorthand constructor for table definitions: Col("File", "s72",
// true) is the primary key column File of type s72.
func Col(name, typ string, key bool) Column {
	t := MustType(typ)
	t.Key = key

	return Column{Name: name, Type: t}
}

// Table is a table with its rows. A cell is nil (NULL), string, int, int32,
// int64 or []byte (binary). An empty string is NULL.
type Table struct {
	Name    string
	Columns []Column
	Rows    [][]any
}

// Keys returns the number of primary key columns.
func (t *Table) Keys() int {
	count := 0

	for _, c := range t.Columns {
		if c.Type.Key {
			count++
		}
	}

	return count
}

// Column returns the index of the named column, or -1.
func (t *Table) Column(name string) int {
	for i, c := range t.Columns {
		if c.Name == name {
			return i
		}
	}

	return -1
}

// Add appends a row.
func (t *Table) Add(values ...any) {
	t.Rows = append(t.Rows, values)
}

// Database is a whole package database.
type Database struct {
	Tables  []*Table
	Streams []cfb.Stream
	// Sources are extra streams that MarshalTo copies from their readers
	// instead of holding them in memory, such as a spooled cabinet. Decode
	// lists the streams it left in the file here with a nil Open.
	Sources []cfb.Source
	// Storages are root-level storages that MarshalTo writes beside the
	// streams, such as embedded language transforms (TransformStorage).
	Storages []cfb.Storage
	Summary  []summary.Property
	Problems []string
	Codepage int
}

// Table returns the named table, or nil.
func (db *Database) Table(name string) *Table {
	for _, t := range db.Tables {
		if t.Name == name {
			return t
		}
	}

	return nil
}

// Stream returns the named extra stream.
func (db *Database) Stream(name string) ([]byte, bool) {
	for _, s := range db.Streams {
		if s.Name == name {
			return s.Data, true
		}
	}

	return nil, false
}

// normalizeCell converts a cell to its canonical Go value: nil, string,
// int or []byte, checked against the column type.
func normalizeCell(col Column, value any) (any, error) { //nolint:cyclop // one pass over the format; splitting would scatter it
	if value == nil {
		if !col.Type.Nullable {
			return nil, fmt.Errorf("column %s is not nullable", col.Name)
		}

		return nil, nil //nolint:nilnil // nil is the NULL cell
	}

	switch col.Type.Kind { //nolint:exhaustive // the default covers the remaining kinds
	case KindString:
		str, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("column %s wants a string, got %T", col.Name, value)
		}

		if str == "" {
			if !col.Type.Nullable {
				return nil, fmt.Errorf("column %s is not nullable", col.Name)
			}

			return nil, nil //nolint:nilnil // an empty string is the NULL cell
		}

		return str, nil
	case KindInt16, KindInt32:
		var num int64

		switch val := value.(type) {
		case int:
			num = int64(val)
		case int32:
			num = int64(val)
		case int64:
			num = val
		case int16:
			num = int64(val)
		case uint16:
			num = int64(val)
		case uint32:
			num = int64(val)
		default:
			return nil, fmt.Errorf("column %s wants an integer, got %T", col.Name, value)
		}

		if col.Type.Kind == KindInt16 && (num < MinInt16 || num > MaxInt16) {
			return nil, fmt.Errorf("column %s: %d is outside %d…%d", col.Name, num, MinInt16, MaxInt16)
		}

		if col.Type.Kind == KindInt32 && (num < MinInt32 || num > MaxInt32) {
			return nil, fmt.Errorf("column %s: %d is outside %d…%d", col.Name, num, MinInt32, MaxInt32)
		}

		return int(num), nil
	default:
		b, ok := value.([]byte)
		if !ok {
			return nil, fmt.Errorf("column %s wants []byte, got %T", col.Name, value)
		}

		return b, nil
	}
}

// Validate checks the table shape: name, column count, key placement and
// every cell. It does not modify the table.
func (t *Table) Validate() error {
	_, err := t.normalized()

	return err
}

// normalized validates the table and returns a copy whose cells hold the
// canonical Go values (nil, string, int or []byte) that the encoders expect.
// The caller's rows are left as they are.
func (t *Table) normalized() (*Table, error) { //nolint:cyclop,gocognit // one pass over the format; splitting would scatter it
	// The limits count characters as msi.dll stores them, UTF-16 units,
	// not UTF-8 bytes: an 11-character Japanese name is 33 bytes.
	if t.Name == "" || nameUnits(t.Name) > MaxTableName {
		return nil, fmt.Errorf("msidb: table name %q must be 1 to %d characters", t.Name, MaxTableName)
	}

	if _, err := EncodeStreamName(t.Name, true); err != nil {
		return nil, err
	}

	if len(t.Columns) == 0 || len(t.Columns) > MaxColumns {
		return nil, fmt.Errorf("msidb: table %s has %d columns, want 1 to %d (the engine rejects the 32nd, MSIDB-07)", t.Name, len(t.Columns), MaxColumns)
	}

	keys, binaries := 0, 0
	seen := make(map[string]bool)

	for i, col := range t.Columns {
		if col.Name == "" || nameUnits(col.Name) > maxColumnName {
			return nil, fmt.Errorf("msidb: table %s: column %d has an invalid name %q", t.Name, i+1, col.Name)
		}

		if seen[col.Name] {
			return nil, fmt.Errorf("msidb: table %s: column %s defined twice", t.Name, col.Name)
		}

		seen[col.Name] = true

		if err := col.Type.check(); err != nil {
			return nil, fmt.Errorf("msidb: table %s: column %s: %w", t.Name, col.Name, err)
		}

		if col.Type.Key {
			if i != keys {
				return nil, fmt.Errorf("msidb: table %s: key column %s must come before the other columns", t.Name, col.Name)
			}

			if col.Type.Kind == KindBinary {
				return nil, fmt.Errorf("msidb: table %s: binary column %s cannot be a key", t.Name, col.Name)
			}

			keys++
		}

		if col.Type.Kind == KindBinary {
			binaries++
		}
	}

	if keys == 0 {
		return nil, fmt.Errorf("msidb: table %s has no primary key", t.Name)
	}

	if binaries > 1 {
		return nil, fmt.Errorf("msidb: table %s has %d binary columns, at most one is allowed", t.Name, binaries)
	}

	rows := make([][]any, len(t.Rows))

	for rowIdx, row := range t.Rows {
		if len(row) != len(t.Columns) {
			return nil, fmt.Errorf("msidb: table %s row %d has %d cells, want %d", t.Name, rowIdx, len(row), len(t.Columns))
		}

		cells := make([]any, len(row))

		for i, c := range t.Columns {
			v, err := normalizeCell(c, row[i])
			if err != nil {
				return nil, fmt.Errorf("msidb: table %s row %d: %w", t.Name, rowIdx, err)
			}

			cells[i] = v
		}

		rows[rowIdx] = cells
	}

	return &Table{Name: t.Name, Columns: t.Columns, Rows: rows}, nil
}
