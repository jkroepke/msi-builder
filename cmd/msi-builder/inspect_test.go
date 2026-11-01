package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/msidb"
)

// inspect marks the key columns that msidb.Table.Keys counts: a key flag
// after a non-key column is not a primary key (MSIDB-21).
func TestPrintTableMarksLeadingKeys(t *testing.T) {
	t.Parallel()

	tbl := &msidb.Table{Name: "T", Columns: []msidb.Column{
		msidb.Col("K", "s16", true), msidb.Col("V", "S16", false), msidb.Col("N", "I2", true),
	}}

	var out bytes.Buffer

	printTable(&out, tbl)

	if lines := strings.Split(out.String(), "\n"); len(lines) < 3 || lines[2] != "s16!\tS16\tI2" {
		t.Errorf("types line %q, want %q", lines, "s16!\tS16\tI2")
	}
}
