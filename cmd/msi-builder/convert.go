package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jkroepke/msi-builder/internal/msidb"
	"github.com/jkroepke/msi-builder/internal/wixconvert"
)

func newConvertCmd() *cobra.Command {
	var (
		out   string
		force bool
		flags convertFlags
	)

	cmd := &cobra.Command{
		Use:   "convert [flags] app.wxs",
		Short: "Convert a WiX source file to a YAML file",
		Long: `Convert reads a WiX source file (.wxs, WiX v3 or later) and writes the
msi.yaml that describes the same package, as far as msi-builder can express
it. It is a starting point for a port, not a byte-for-byte translation:
everything it cannot convert, such as includes, bind variables, hand-built
dialogs and extensions without a counterpart, becomes a "# TODO" comment in
the YAML file and a line on stderr.

Preprocessor blocks become template actions: <?if?>, <?ifdef?>, <?elseif?>
and <?else?> become {{ if }}, {{ if has "NAME" }} and {{ else }}, and
<?foreach?> becomes {{ range }} over split ";". A <?define?> inside a block
becomes a var whose value tests the block, and an <?error?> becomes fail.
Convert writes a block as a template when every element in it is a
component, a file, registry value or key, environment variable, plain
property, a component group of these, or a reference that puts components
in a feature or group. Of any other block, it converts only the branch that
a build without -d variables takes, with the file's defines and the platform
--arch names, or the first branch not known to be false when that depends
on a -d variable, and leaves a "# TODO".

A component whose Guid WiX generates (Guid="*" or none) keeps the code WiX
gives it: convert computes it from the key file or key registry value as
WiX does, for the platform --arch names (without it, codes that depend on the
platform keep a "# TODO") and, with --bcgg, as
WiX 6 and later do with -bcgg. Each file a WiX Files glob harvests keeps its code in the
guids of the folder source the glob became, when the folder is next to the
source file. With --msi and the package WiX built from the source file,
convert takes the codes from that package instead, finding each component
by its ID, its key file or its registry key path. A component whose code
convert finds neither way gets a "# TODO" comment: msi-builder generates
another code for it, and an upgrade that removes the old version after
installing the new one (MajorUpgrade Schedule="afterInstallExecute", and
presumably "afterInstallFinalize") deletes the files of every component
whose code changed.

The YAML file goes to stdout, or to the file --output names. Review it, then
check it with msi-builder validate.`,
		Example: `  msi-builder convert app.wxs
  msi-builder convert -o msi.yaml app.wxs
  msi-builder convert --arch x64 -o msi.yaml app.wxs
  msi-builder convert --msi app-1.4.0.msi -o msi.yaml app.wxs`,
		Args:              exactlyOne("app.wxs"),
		ValidArgsFunction: fileArg("wxs"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if force && out == "" {
				return usageError{errors.New("--force needs --output")}
			}

			return convert(cmd, args[0], flags, out, force)
		},
	}

	cmd.Flags().StringVarP(&out, "output", "o", "", "write the YAML file here instead of stdout")
	cmd.Flags().StringVar(&flags.built, "msi", "", "keep the component codes of this package, built by WiX from app.wxs")
	cmd.Flags().StringVar(&flags.arch, "arch", "", "the platform WiX built for (wix build -arch): x86, x64 or arm64; also sets package.platform")
	cmd.Flags().BoolVar(&flags.bcgg, "bcgg", false, "WiX 6 or later built the package with -bcgg (WiX 3 codes for registry key paths)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing output file")
	mustComplete(cmd.MarkFlagFilename("output", "yaml", "yml"))
	mustComplete(cmd.MarkFlagFilename("msi", "msi"))
	mustComplete(cmd.RegisterFlagCompletionFunc("arch", cobra.FixedCompletions([]string{"x86", "x64", "arm64"}, cobra.ShellCompDirectiveNoFileComp)))

	return cmd
}

// convertFlags are the flags of convert that tune the conversion.
type convertFlags struct {
	built, arch string
	bcgg        bool
}

func convert(cmd *cobra.Command, path string, flags convertFlags, out string, force bool) error {
	dir := filepath.Dir(path)
	opts := wixconvert.Options{
		Source: filepath.Base(path), Exists: existsBeside(dir), FS: os.DirFS(dir), Arch: flags.arch, BCGG: flags.bcgg,
	}

	if flags.built != "" {
		pkg, err := builtPackage(flags.built)
		if err != nil {
			return err
		}

		opts.Built = pkg
	}

	input, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}

	defer func() { _ = input.Close() }()

	res, err := wixconvert.Convert(input, opts)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	stderr := cmd.ErrOrStderr()

	for _, n := range res.Notes {
		if n.Line > 0 {
			_, _ = fmt.Fprintf(stderr, "%s:%d: TODO: %s\n", path, n.Line, n.Message)
		} else {
			_, _ = fmt.Fprintf(stderr, "%s: TODO: %s\n", path, n.Message)
		}
	}

	if out == "" {
		_, err := cmd.OutOrStdout().Write(res.YAML)
		if err != nil {
			return fmt.Errorf("write: %w", err)
		}

		return nil
	}

	if err := writeNew(out, res.YAML, force); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stderr, "%s: written, %d TODO comments to review\n", out, len(res.Notes))

	return nil
}

// writeNew writes data to path. It refuses an existing file unless force is
// set.
func writeNew(path string, data []byte, force bool) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !force {
		flags |= os.O_EXCL
	}

	file, err := os.OpenFile(path, flags, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s exists; pass --force to overwrite it", path)
	}

	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	if _, err := file.Write(data); err != nil {
		_ = file.Close()

		return fmt.Errorf("write: %w", err)
	}

	if err := file.Close(); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	return nil
}

// builtPackage reads the components of a package, the long names of their
// key files or their registry key paths, and the folders.
func builtPackage(path string) (*wixconvert.BuiltPackage, error) {
	database, closePackage, err := openPackage(path)
	if err != nil {
		return nil, err
	}

	defer closePackage()

	components := database.Table("Component")
	if components == nil {
		return nil, fmt.Errorf("%s has no Component table", path)
	}

	names, registry := builtFileNames(database), builtRegistry(database)

	cols := []int{
		components.Column("Component"), components.Column("ComponentId"),
		components.Column("Directory_"), components.Column("Attributes"), components.Column("KeyPath"),
	}
	if slices.Contains(cols, -1) {
		return nil, fmt.Errorf("%s: the Component table lacks a standard column", path)
	}

	pkg := &wixconvert.BuiltPackage{Name: filepath.Base(path), Directories: builtDirectories(database)}

	for _, row := range components.Rows {
		comp := wixconvert.BuiltComponent{
			ID: cellString(row, cols[0]), Code: cellString(row, cols[1]), Directory: cellString(row, cols[2]),
		}

		// Bits 4 and 32: the key path is a registry value or an ODBC
		// data source, not a file.
		attributes, ok := cellInt(row, cols[3])

		switch {
		case ok && attributes&(4|32) == 0:
			comp.KeyFile = names[cellString(row, cols[4])]
		case ok && attributes&4 != 0:
			comp.KeyRegistry = registry[cellString(row, cols[4])]
		}

		pkg.Components = append(pkg.Components, comp)
	}

	return pkg, nil
}

// builtFileNames reads the long name of each file by its key.
func builtFileNames(database *msidb.Database) map[string]string {
	names := make(map[string]string)

	files := database.Table("File")
	if files == nil {
		return names
	}

	key, name := files.Column("File"), files.Column("FileName")
	for _, row := range files.Rows {
		long := cellString(row, name)
		if _, after, ok := strings.Cut(long, "|"); ok {
			long = after
		}

		names[cellString(row, key)] = long
	}

	return names
}

// builtRegistry reads the Registry table: the Root, Key and Name of each
// row by its key.
func builtRegistry(database *msidb.Database) map[string]*wixconvert.BuiltRegistry {
	table := database.Table("Registry")
	if table == nil {
		return nil
	}

	key, root, regKey, name := table.Column("Registry"), table.Column("Root"), table.Column("Key"), table.Column("Name")
	rows := make(map[string]*wixconvert.BuiltRegistry, len(table.Rows))

	for _, row := range table.Rows {
		code, ok := cellInt(row, root)
		if !ok {
			continue
		}

		rows[cellString(row, key)] = &wixconvert.BuiltRegistry{Root: code, Key: cellString(row, regKey), Name: cellString(row, name)}
	}

	return rows
}

// builtDirectories reads the Directory table: the ID, the parent and the
// long target name of each folder.
func builtDirectories(database *msidb.Database) []wixconvert.BuiltDirectory {
	table := database.Table("Directory")
	if table == nil {
		return nil
	}

	key, parent, name := table.Column("Directory"), table.Column("Directory_Parent"), table.Column("DefaultDir")
	dirs := make([]wixconvert.BuiltDirectory, 0, len(table.Rows))

	for _, row := range table.Rows {
		dirs = append(dirs, wixconvert.BuiltDirectory{
			ID: cellString(row, key), Parent: cellString(row, parent), Name: targetName(cellString(row, name)),
		})
	}

	return dirs
}

// existsBeside returns a function that reports whether a file exists at a
// slash path, relative to dir unless it is absolute.
func existsBeside(dir string) func(string) bool {
	return func(rel string) bool {
		file := filepath.FromSlash(rel)
		if !filepath.IsAbs(file) {
			file = filepath.Join(dir, file)
		}

		_, err := os.Stat(file)

		return err == nil
	}
}

// targetName returns the long target name of a DefaultDir value,
// target[:source], each short|long.
func targetName(defaultDir string) string {
	target, _, _ := strings.Cut(defaultDir, ":")
	if _, long, ok := strings.Cut(target, "|"); ok {
		return long
	}

	return target
}

func cellString(row []any, col int) string {
	if col < 0 || col >= len(row) {
		return ""
	}

	if s, ok := row[col].(string); ok {
		return s
	}

	return ""
}

func cellInt(row []any, col int) (int, bool) {
	if col < 0 || col >= len(row) {
		return 0, false
	}

	switch value := row[col].(type) {
	case int:
		return value, true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case nil:
		return 0, true
	}

	return 0, false
}
