// Command msi-builder builds Windows Installer packages from a YAML description.
//
//	msi-builder build [-o out.msi] [--var NAME=VALUE]... [--env] msi.yaml
//	msi-builder validate [--var NAME=VALUE]... [--env] msi.yaml
//	msi-builder render [--var NAME=VALUE]... [--env] msi.yaml
//	msi-builder inspect [--table NAME] package.msi
//	msi-builder convert [-o msi.yaml] app.wxs
//	msi-builder sign [signing options] [-o out.msi] package.msi
//	msi-builder verify package.msi
//	msi-builder completion bash|fish|powershell|zsh
//	msi-builder version
//
// docs/cli holds the reference for every command; it is generated from the
// command tree (go generate ./cmd/msi-builder).
package main

//go:generate go test -run ^TestDocs$ -update .

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// Set by the linker at release time.
var (
	version = "dev"
	commit  = "none"    //nolint:gochecknoglobals // set by the linker
	date    = "unknown" //nolint:gochecknoglobals // set by the linker
)

// Exit codes of run.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the command line and returns the exit code: 0 on success,
// 1 when the command failed and 2 for a usage error. Help goes to stdout,
// errors and diagnostics to stderr.
func run(args []string, stdout, stderr io.Writer) int {
	root := newRootCmd(stdout, stderr)

	// A nil slice makes cobra read os.Args.
	root.SetArgs(append(make([]string, 0, len(args)), args...))

	cmd, err := root.ExecuteC()
	if err == nil {
		return exitOK
	}

	_, _ = fmt.Fprintf(stderr, "msi-builder: %v\n", err)

	// Errors of the root command are unknown commands.
	if cmd != root && !isUsageError(err) {
		return exitFailure
	}

	_, _ = fmt.Fprintf(stderr, "Run '%s --help' for usage.\n", cmd.CommandPath())

	return exitUsage
}

// usageError marks a wrong command line: an unknown command or flag, a bad
// flag value or the wrong number of arguments.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }

func (e usageError) Unwrap() error { return e.err }

func isUsageError(err error) bool {
	var usage usageError

	return errors.As(err, &usage)
}

func newRootCmd(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:   "msi-builder",
		Short: "Build Windows Installer (MSI) packages from a YAML file",
		Long: `msi-builder builds Windows Installer (MSI) packages from a single YAML file,
signs them with Authenticode and inspects existing packages. It is pure Go
and builds the same packages on Linux, macOS and Windows.`,
		Version: version,
		// Without Args, cobra rejects unknown commands.
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 { // after --
				return usageError{fmt.Errorf("unknown command %q", args[0])}
			}

			_, _ = io.WriteString(stderr, cmd.UsageString())

			return usageError{errors.New("missing command")}
		},
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	// Declared here so that cobra adds no -v shorthand.
	root.Flags().Bool("version", false, "print the version")
	root.SetHelpCommand(newHelpCmd())
	// The root and completion commands only run to report a usage error, so
	// their usage shows the "[command]" line alone.
	root.SetUsageTemplate(strings.Replace(root.UsageTemplate(),
		"{{if .Runnable}}", "{{if and .Runnable (not .HasAvailableSubCommands)}}", 1))
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetVersionTemplate(versionLine())
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })

	root.AddCommand(
		newBuildCmd(),
		newValidateCmd(),
		newRenderCmd(),
		newInspectCmd(),
		newConvertCmd(),
		newSignCmd(),
		newVerifyCmd(),
		newCompletionCmd(),
		newVersionCmd(),
	)

	return root
}

func versionLine() string {
	return fmt.Sprintf("msi-builder %s (commit %s, built %s)\n", version, commit, date)
}

// newHelpCmd replaces cobra's help command, which prints an unknown topic
// to stdout and exits 0.
func newHelpCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "help [command]",
		Short:             "Show the help of a command",
		Long:              "Help shows the help of a command, such as msi-builder help build.",
		ValidArgsFunction: completeHelp,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, rest, err := cmd.Root().Find(args)
			if err != nil || len(rest) != 0 {
				return usageError{fmt.Errorf("unknown help topic %q", strings.Join(args, " "))}
			}

			target.InitDefaultHelpFlag()
			target.InitDefaultVersionFlag()

			return target.Help()
		},
	}
}

// completeHelp offers the subcommands of the command named so far.
func completeHelp(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	parent, _, err := cmd.Root().Find(args)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var names []cobra.Completion

	for _, sub := range parent.Commands() {
		if sub.IsAvailableCommand() && strings.HasPrefix(sub.Name(), toComplete) {
			names = append(names, cobra.CompletionWithDesc(sub.Name(), sub.Short))
		}
	}

	return names, cobra.ShellCompDirectiveNoFileComp
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "version",
		Short:             "Print the version",
		Args:              noArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _ = io.WriteString(cmd.OutOrStdout(), versionLine())

			return nil
		},
	}
}

func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usageError{fmt.Errorf("%s takes no arguments, got %q", cmd.Name(), args)}
	}

	return nil
}

// exactlyOne accepts one positional argument, described by what.
func exactlyOne(what string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return usageError{fmt.Errorf("%s wants exactly one %s, got %d arguments", cmd.Name(), what, len(args))}
		}

		return nil
	}
}

// fileArg completes the single positional argument with files that have
// one of the extensions.
func fileArg(extensions ...string) cobra.CompletionFunc {
	return func(_ *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) != 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		return extensions, cobra.ShellCompDirectiveFilterFileExt
	}
}

// fixedValues completes a flag with a fixed set of values.
func fixedValues(values ...string) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return slices.Clone(values), cobra.ShellCompDirectiveNoFileComp
	}
}

// mustComplete registers flag completions; errors are programming errors
// (an unknown flag name) that every test run would catch.
func mustComplete(err error) {
	if err != nil {
		panic(err)
	}
}
