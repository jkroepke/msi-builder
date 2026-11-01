package main

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jkroepke/msi-builder/internal/cfb"
	"github.com/jkroepke/msi-builder/internal/msidb"
)

func newInspectCmd() *cobra.Command {
	var table string

	cmd := &cobra.Command{
		Use:   "inspect [flags] package.msi",
		Short: "Print the tables, streams and summary of a package",
		Long: `Inspect prints the code page, the summary information, the streams and
every table of a package, or a single table with --table. Columns are
separated by tabs; the second line of a table holds the column types, with
"!" marking key columns. Control characters in a value are shown as their
control pictures (U+2400 to U+2421), so a newline appears as "␊". Cabinets
stay in the file and are listed by size.`,
		Example: `  msi-builder inspect hello.msi
  msi-builder inspect --table File hello.msi`,
		Args:              exactlyOne("package"),
		ValidArgsFunction: fileArg("msi"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return inspect(cmd.OutOrStdout(), args[0], table)
		},
	}

	cmd.Flags().StringVar(&table, "table", "", "print only this table")
	mustComplete(cmd.RegisterFlagCompletionFunc("table", completeTables))

	return cmd
}

// openPackage decodes the package at path lazily.
func openPackage(path string) (*msidb.Database, func(), error) {
	pkg, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open: %w", err)
	}

	stat, err := pkg.Stat()
	if err != nil {
		_ = pkg.Close()

		return nil, nil, fmt.Errorf("stat: %w", err)
	}

	file, err := cfb.Open(pkg, stat.Size())
	if err != nil {
		_ = pkg.Close()

		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}

	database, err := msidb.Decode(file)
	if err != nil {
		_ = pkg.Close()

		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}

	return database, func() { _ = pkg.Close() }, nil
}

// completeTables offers the table names of the package given as argument.
func completeTables(_ *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) != 1 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	database, closePackage, err := openPackage(args[0])
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	defer closePackage()

	names := make([]cobra.Completion, 0, len(database.Tables))
	for _, t := range database.Tables {
		names = append(names, t.Name)
	}

	return names, cobra.ShellCompDirectiveNoFileComp
}

func inspect(stdout io.Writer, path, table string) error {
	database, closePackage, err := openPackage(path)
	if err != nil {
		return err
	}

	defer closePackage()

	if table != "" {
		t := database.Table(table)
		if t == nil {
			return fmt.Errorf("no table %s", table)
		}

		printTable(stdout, t)

		return nil
	}

	_, _ = fmt.Fprintf(stdout, "codepage: %d\n", database.Codepage)

	for _, p := range database.Summary {
		_, _ = fmt.Fprintf(stdout, "summary %2d: %s\n", p.ID, formatValue(p.Value))
	}

	names := make([]string, 0, len(database.Streams)+len(database.Sources))
	for _, s := range database.Streams {
		names = append(names, fmt.Sprintf("%s (%d bytes)", s.Name, len(s.Data)))
	}

	for _, s := range database.Sources {
		names = append(names, fmt.Sprintf("%s (%d bytes)", s.Name, s.Size))
	}

	slices.Sort(names)

	if len(names) > 0 {
		_, _ = fmt.Fprintf(stdout, "streams: %s\n", strings.Join(names, ", "))
	}

	for _, p := range database.Problems {
		_, _ = fmt.Fprintf(stdout, "problem: %s\n", p)
	}

	for _, t := range database.Tables {
		_, _ = fmt.Fprintln(stdout)
		printTable(stdout, t)
	}

	return nil
}

func printTable(out io.Writer, tbl *msidb.Table) {
	cols := make([]string, len(tbl.Columns))
	types := make([]string, len(tbl.Columns))

	// Only the leading key columns are keys, as msidb.Table.Keys and msi.dll
	// count them (MSIDB-21).
	keys := tbl.Keys()

	for i, c := range tbl.Columns {
		cols[i] = c.Name
		types[i] = c.Type.String()

		if i < keys {
			types[i] += "!"
		}
	}

	_, _ = fmt.Fprintf(out, "== %s (%d rows)\n%s\n%s\n", tbl.Name, len(tbl.Rows), strings.Join(cols, "\t"), strings.Join(types, "\t"))

	for _, r := range tbl.Rows {
		cells := make([]string, len(r))
		for i, v := range r {
			cells[i] = formatValue(v)
		}

		_, _ = fmt.Fprintln(out, strings.Join(cells, "\t"))
	}
}

func formatValue(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return showControls(val)
	case int:
		return strconv.Itoa(val)
	case []byte:
		return fmt.Sprintf("[binary %d bytes]", len(val))
	case time.Time:
		return val.UTC().Format(time.RFC3339)
	default:
		return fmt.Sprint(val)
	}
}

// showControls replaces the C0 control characters and DEL in a value with
// their control pictures (U+2400 to U+2421), so that a tab, a newline or the
// U+001F separator in the private properties of the embedded custom
// action DLL keeps one row on one line and the output text.
func showControls(value string) string {
	return strings.Map(func(char rune) rune {
		if char < 0x20 {
			return 0x2400 + char
		}

		if char == 0x7f {
			return 0x2421
		}

		return char
	}, value)
}
