package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jkroepke/msi-builder/internal/spool"
	"github.com/jkroepke/msi-builder/pkg/builder"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// vars collects repeated --var NAME=VALUE flags.
type vars map[string]string

func (v vars) String() string { return "" }

func (v vars) Type() string { return "NAME=VALUE" }

func (v vars) Set(s string) error {
	name, value, ok := strings.Cut(s, "=")
	if !ok || name == "" {
		return fmt.Errorf("want NAME=VALUE, got %q", s)
	}

	v[name] = value

	return nil
}

// loadFlags are the flags shared by build and validate.
type loadFlags struct {
	vars vars
	env  bool
}

func (l *loadFlags) register(cmd *cobra.Command) {
	l.vars = make(vars)
	cmd.Flags().Var(l.vars, "var", "set a variable read as {{ .NAME }}; repeatable")
	cmd.Flags().BoolVar(&l.env, "env", false, "let environment variables fill {{ .NAME }}")
	mustComplete(cmd.RegisterFlagCompletionFunc("var", cobra.NoFileCompletions))
}

func (l *loadFlags) load(path string) (*config.Config, error) {
	cfg, err := config.Load(path, l.options())
	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}

	return cfg, nil
}

func (l *loadFlags) options() config.Options {
	opts := config.Options{Vars: l.vars}
	if l.env {
		opts.LookupEnv = os.LookupEnv
	}

	return opts
}

func printDiagnostics(w io.Writer, ds config.Diagnostics) {
	for _, d := range ds {
		_, _ = fmt.Fprintln(w, d.String())
	}
}

func newValidateCmd() *cobra.Command {
	var load loadFlags

	cmd := &cobra.Command{
		Use:   "validate [flags] msi.yaml",
		Short: "Check a YAML file without building a package",
		Long: `Validate loads msi.yaml, checks it against the schema and runs the
configuration checks of a build. It reads no input files: checks that need
them, such as PE versions, run only in build. Diagnostics go to stderr; the
exit code is 1 when there is an error.`,
		Example:           "  msi-builder validate --var VERSION=1.2.3 msi.yaml",
		Args:              exactlyOne("msi.yaml"),
		ValidArgsFunction: fileArg("yaml", "yml"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := load.load(args[0])
			if err != nil {
				return err
			}

			ds := config.Validate(cfg)
			printDiagnostics(cmd.ErrOrStderr(), ds)

			if err := ds.Err(); err != nil {
				return errors.New("validation failed")
			}

			return nil
		},
	}

	load.register(cmd)

	return cmd
}

func newRenderCmd() *cobra.Command {
	var load loadFlags

	cmd := &cobra.Command{
		Use:   "render [flags] msi.yaml",
		Short: "Print the YAML that a file renders to",
		Long: `Render runs msi.yaml through Go's text/template, as build and validate do
before they read it, and prints the YAML that comes out, the vars header
as it is. A YAML error names a line of this output.`,
		Example:           "  msi-builder render --var ARCH=arm64 msi.yaml",
		Args:              exactlyOne("msi.yaml"),
		ValidArgsFunction: fileArg("yaml", "yml"),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err //nolint:wrapcheck // the error names the file
			}

			out, err := config.Render(data, load.options())
			if err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}

			_, err = cmd.OutOrStdout().Write(out)

			return err //nolint:wrapcheck // writing to stdout
		},
	}

	load.register(cmd)

	return cmd
}

// buildFlags are the flags of build.
type buildFlags struct {
	load       loadFlags
	signing    signFlags
	out        string
	cfbVersion int
	skipVerify bool
	quiet      bool
}

func newBuildCmd() *cobra.Command {
	var flags buildFlags

	cmd := &cobra.Command{
		Use:   "build [flags] msi.yaml",
		Short: "Build a package from a YAML file",
		Long: `Build writes the package that msi.yaml describes. Relative paths in the
YAML file resolve against its directory. The package is read back and
compared with its sources unless --skip-verify is given, and it is signed
when a key is given (see the signing flags and docs/signing.md).

Diagnostics go to stderr; on success one summary line goes to stdout.`,
		Example: `  msi-builder build -o hello.msi msi.yaml
  msi-builder build --var VERSION=1.2.3 --var ARCH=x64 msi.yaml
  msi-builder build --pfx codesign.pfx --password-env PFX_PASSWORD \
    --timestamp-url http://timestamp.digicert.com -o hello.msi msi.yaml`,
		Args:              exactlyOne("msi.yaml"),
		ValidArgsFunction: fileArg("yaml", "yml"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return flags.run(cmd, args[0])
		},
	}

	flags.load.register(cmd)
	cmd.Flags().StringVarP(&flags.out, "output", "o", "",
		"output file (default: <product name>-<version>.msi next to the YAML file, characters a file name cannot hold replaced by _)")
	cmd.Flags().IntVar(&flags.cfbVersion, "cfb", 4, "compound file version, 3 (512-byte sectors) or 4 (4096-byte sectors)")
	cmd.Flags().BoolVar(&flags.skipVerify, "skip-verify", false, "do not read the finished package back")
	cmd.Flags().BoolVarP(&flags.quiet, "quiet", "q", false, "print nothing on success")
	flags.signing.register(cmd)

	mustComplete(cmd.MarkFlagFilename("output", "msi"))
	mustComplete(cmd.RegisterFlagCompletionFunc("cfb", fixedValues("3", "4")))

	return cmd
}

func (f *buildFlags) run(cmd *cobra.Command, path string) error {
	if err := f.check(); err != nil {
		return err
	}

	cfg, err := f.load.load(path)
	if err != nil {
		return err
	}

	out := f.output(cfg)

	// Signing works on the whole package, so a signed build streams to a
	// temporary file first and the signed bytes replace out atomically; a
	// package already at out survives a failed signing. Cabinets beside the
	// package go to a temporary folder with it and move only together with
	// the signed package.
	target := out

	if f.signing.enabled() {
		var cleanup func()
		if target, cleanup, err = tempPackage(out, !cfg.Package.CabinetsEmbedded()); err != nil {
			return err
		}

		defer cleanup()
	}

	opts, err := f.builderOptions(cfg, target)
	if err != nil {
		return err
	}

	res, err := builder.Build(cfg, opts)

	printDiagnostics(cmd.ErrOrStderr(), res.Diagnostics)

	if err != nil {
		return buildFailed(res, err)
	}

	if f.signing.enabled() {
		if res.Size, err = f.signInto(cmd.Context(), target, out, cfg.Product.Name, res.Cabinets); err != nil {
			return err
		}
	}

	if !f.quiet {
		printResult(cmd.OutOrStdout(), out, res)
	}

	return nil
}

// buildFailed is the error of a failed build: the diagnostics are already
// printed.
func buildFailed(res *builder.Result, err error) error {
	if res.Diagnostics.Err() != nil {
		return errors.New("build failed")
	}

	return fmt.Errorf("build failed: %w", err)
}

// builderOptions are the build options for a package written to target.
// Cabinets beside the package are signed as they are written, so that the
// package can list their digests.
func (f *buildFlags) builderOptions(cfg *config.Config, target string) (builder.Options, error) {
	signCabinets, err := f.signing.cabinetOptions(cfg)
	if err != nil {
		return builder.Options{}, err
	}

	return builder.Options{
		CFBVersion:   f.cfbVersion,
		ToolName:     "msi-builder " + version,
		SkipVerify:   f.skipVerify,
		Output:       target,
		SignCabinets: signCabinets,
	}, nil
}

// printResult reports the package at out and the cabinets beside it.
func printResult(w io.Writer, out string, res *builder.Result) {
	_, _ = fmt.Fprintf(w, "%s: %d bytes, %d files, ProductCode %s, PackageCode %s\n",
		out, res.Size, len(res.Files), res.ProductCode, res.PackageCode)

	for _, cabinet := range res.Cabinets {
		_, _ = fmt.Fprintf(w, "%s: %d bytes, cabinet beside the package\n", cabinet.Path, cabinet.Size)
	}
}

// check refuses flag values before any file is read.
func (f *buildFlags) check() error {
	if f.cfbVersion != 3 && f.cfbVersion != 4 {
		return usageError{fmt.Errorf("--cfb: want 3 or 4, got %d", f.cfbVersion)}
	}

	return f.signing.check()
}

// output is the package path: --output, or <name>-<version>.msi next to
// the YAML file.
func (f *buildFlags) output(cfg *config.Config) string {
	if f.out != "" {
		return f.out
	}

	return filepath.Join(cfg.BaseDir, defaultFileName(cfg.Product.Name+"-"+cfg.Product.Version)+".msi")
}

// defaultFileName turns a product name into a file name in the YAML file's
// folder: a display name may hold path separators or characters Windows
// does not allow in a file name, and must neither pick another folder nor
// fail the build. Leading dots, spaces and dashes go too, so that the name
// is never "." or "..", nor hidden, nor read as a flag.
func defaultFileName(name string) string {
	safe := strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}

		return r
	}, name)

	safe = strings.TrimLeft(safe, ". -")
	if safe == "" {
		return "package"
	}

	return safe
}

// tempPackage returns a temporary path next to out for the unsigned
// package, in a temporary folder of its own when cabinets are written beside
// it, and the function that removes what is left there.
func tempPackage(out string, withCabinets bool) (string, func(), error) {
	if withCabinets {
		// The cabinets move next to the package's real file, so the
		// temporary folder goes there too, on the same file system, and
		// the package keeps that file's name for the builder's name check.
		if resolved, err := filepath.EvalSymlinks(out); err == nil {
			out = resolved
		}

		dir, err := os.MkdirTemp(filepath.Dir(out), ".msi-builder-*")
		if err != nil {
			return "", nil, fmt.Errorf("create a temporary folder: %w", err)
		}

		return filepath.Join(dir, filepath.Base(out)), func() { _ = os.RemoveAll(dir) }, nil
	}

	tmp, err := os.CreateTemp(filepath.Dir(out), ".msi-builder-*.unsigned")
	if err != nil {
		return "", nil, fmt.Errorf("create a temporary file: %w", err)
	}

	_ = tmp.Close()

	return tmp.Name(), func() { _ = os.Remove(tmp.Name()) }, nil
}

// signInto signs the unsigned package at target, writes the result to out and
// returns its size. Cabinets built beside target move next to out together
// with the signed package, all or none, and their paths in cabinets are
// updated.
func (f *buildFlags) signInto(ctx context.Context, target, out, product string, cabinets []builder.Cabinet) (int64, error) {
	pkg, err := os.ReadFile(target)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", target, err)
	}

	signed, err := f.signing.signBuild(ctx, pkg, product)
	if err != nil {
		return 0, err
	}

	if len(cabinets) == 0 {
		if err := writeAtomic(out, signed); err != nil {
			return 0, err
		}

		return int64(len(signed)), nil
	}

	if err := commitSigned(target, out, signed, cabinets); err != nil {
		return 0, err
	}

	return int64(len(signed)), nil
}

// commitSigned writes the signed package over target, which is alone in its
// temporary folder with the cabinets, and moves them all next to out at
// once.
func commitSigned(target, out string, signed []byte, cabinets []builder.Cabinet) error {
	if err := writeAtomic(target, signed); err != nil {
		return err
	}

	link := filepath.Base(out)
	if resolved, err := filepath.EvalSymlinks(out); err == nil {
		out = resolved
	}

	// The builder saw the name of the symlink's target; a cabinet must not
	// replace the symlink either.
	for _, cabinet := range cabinets {
		if strings.EqualFold(cabinet.Name, link) {
			return fmt.Errorf("the cabinet %s would replace the package; choose another package.cabinet or output name", cabinet.Name)
		}
	}

	if info, err := os.Stat(out); err == nil && info.Mode().IsRegular() {
		if err := os.Chmod(target, info.Mode().Perm()); err != nil {
			return fmt.Errorf("set the mode of %s: %w", out, err)
		}
	}

	moves := make([]spool.Move, 0, len(cabinets)+1)

	for i, cabinet := range cabinets {
		dest := filepath.Join(filepath.Dir(out), cabinet.Name)
		moves = append(moves, spool.Move{From: cabinet.Path, To: dest})
		cabinets[i].Path = dest
	}

	if err := spool.Commit(append(moves, spool.Move{From: target, To: out})); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}

	return nil
}
