package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"charm.land/fang/v2"
	"github.com/spf13/cobra"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/units/catalog"
	"github.com/sathyassn/agent-workstation-kit/internal/version"
)

// Names declared once for the whole tree. Verbs, stages and non-unit
// commands come from contract; these are the CLI's own words.
const (
	programName = "wskit"

	flagJSON    = "json"
	flagPlain   = "plain"
	flagVerbose = "verbose"
	flagQuiet   = "quiet"
	flagApply   = "apply"
	flagConfig  = "config"
	flagStage   = "stage"
	flagTo      = "to"

	cmdVersion    = "version"
	cmdCompletion = "completion"
	cmdConfigGet  = "get"
	cmdConfigSet  = "set"
	cmdConfigVal  = "validate"

	groupWorkflow = "workflow"
	groupConfig   = "config"
	groupUnits    = "units"
)

// Shell is a shell the completion command generates a script for.
type Shell string

// Shells with generated completion (ADR 0005 section 2b).
const (
	ShellBash Shell = "bash"
	ShellZsh  Shell = "zsh"
	ShellFish Shell = "fish"
)

// AllShells returns every Shell in a stable order.
func AllShells() []Shell { return []Shell{ShellBash, ShellZsh, ShellFish} }

// Options configures one CLI run. Zero values use the process's own streams,
// environment and build version.
type Options struct {
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Getenv  func(string) string
	Version string
}

func (o Options) withDefaults() Options {
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Version == "" {
		o.Version = version.String()
	}
	return o
}

// sharedFlags holds the flags every command shares, declared once on the
// root (ADR 0005 section 2b, "CLI conventions").
type sharedFlags struct {
	json    bool
	plain   bool
	verbose bool
	quiet   bool
	apply   bool
	config  string
	stage   stageValue
}

// Execute runs the CLI with args (without the program name) and returns the
// error that decides the exit code; map it with ExitCodeFor. It writes the
// error itself in the selected output mode and never exits.
func Execute(ctx context.Context, args []string, opts Options) error {
	opts = opts.withDefaults()
	root := NewRootCommand(opts)
	root.SetArgs(args)

	mode := resolveOutputMode(peekOutputFlags(args), opts.Getenv, isTerminal(opts.Stdout), isTerminal(opts.Stderr))
	var err error
	if mode == ModeTUI {
		// fang styles help and errors and prints the error itself. Signals
		// stay with the caller, so they reach the engine's one cancel path.
		err = fang.Execute(ctx, root,
			fang.WithoutVersion(), fang.WithoutCompletions(), fang.WithoutManpage())
	} else {
		err = root.ExecuteContext(ctx)
	}
	err = classify(err)
	if err == nil {
		return nil
	}
	if werr := writeError(mode, opts, err); werr != nil {
		return errors.Join(err, werr)
	}
	return err
}

// writeError reports err in the output mode: JSON on standard output, one
// plain line on standard error, or nothing when fang has already shown it.
func writeError(mode OutputMode, opts Options, err error) error {
	switch mode {
	case ModeJSON:
		return writeJSON(opts.Stdout, ToCLIError(err))
	case ModePlain:
		if _, werr := io.WriteString(opts.Stderr, programName+": "+err.Error()+"\n"); werr != nil {
			return fmt.Errorf("write error: %w", werr)
		}
		return nil
	case ModeTUI:
		return nil
	default:
		return nil
	}
}

// NewRootCommand builds the whole command tree: SPC-001's command matrix,
// one command per catalog unit with the unit verbs, version and completion.
func NewRootCommand(opts Options) *cobra.Command {
	opts = opts.withDefaults()
	flags := &sharedFlags{}
	root := newGroupCommand(programName, "Set up and maintain AI agent workstations")
	root.Long = "wskit plans, applies, verifies and reverts the setup of Ubuntu and macOS " +
		"workstations for AI coding agents. Changes are previewed unless --apply is given."
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetIn(opts.Stdin)
	root.SetOut(opts.Stdout)
	root.SetErr(opts.Stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &Error{Class: ClassUsage, Err: fmt.Errorf("%w: %w", ErrUsage, err)}
	})

	addSharedFlags(root, flags)
	root.AddGroup(
		&cobra.Group{ID: groupWorkflow, Title: "Commands:"},
		&cobra.Group{ID: groupConfig, Title: "Configuration commands:"},
		&cobra.Group{ID: groupUnits, Title: "Unit commands (wskit UNIT VERB):"},
	)
	for _, c := range matrixCommands() {
		if c.GroupID == "" {
			c.GroupID = groupWorkflow
		}
		root.AddCommand(c)
	}
	addUnitCommands(root)
	root.AddCommand(newVersionCommand(opts.Version, flags), newCompletionCommand())
	return root
}

func addSharedFlags(root *cobra.Command, f *sharedFlags) {
	pf := root.PersistentFlags()
	pf.BoolVar(&f.json, flagJSON, false, "write the result as versioned JSON on standard output and events as JSON lines on standard error")
	pf.BoolVar(&f.plain, flagPlain, false, "write plain lines with no styling and no prompts (also WSKIT_PLAIN=1)")
	pf.BoolVar(&f.verbose, flagVerbose, false, "show more detail")
	pf.BoolVar(&f.quiet, flagQuiet, false, "show only results and errors")
	pf.BoolVar(&f.apply, flagApply, false, "make the changes; without it, commands only preview")
	pf.StringVar(&f.config, flagConfig, "", "read configuration from this file")
	pf.Var(&f.stage, flagStage, "select one setup stage by its ID")
	root.MarkFlagsMutuallyExclusive(flagVerbose, flagQuiet)
	// Registration fails only for a missing flag, which the line above rules out.
	_ = root.RegisterFlagCompletionFunc(flagStage, completeStages)
}

// matrixCommands returns the top-level commands of SPC-001's command matrix.
func matrixCommands() []*cobra.Command {
	revert := newStubCommand(contract.VerbRevert.String(), "Revert to a restore point or to the baseline")
	revert.Flags().String(flagTo, "", "the restore point or baseline to revert to")

	config := newGroupCommand(contract.CmdConfig.String(), "Show, change and check layered configuration")
	config.GroupID = groupConfig
	interview := newStubCommand(contract.CmdInterview.String(), "Answer questions to write a configuration")
	interview.GroupID = groupConfig
	migrate := newStubCommand(contract.CmdMigrate.String(), "Migrate configuration to the current schema")
	migrate.GroupID = groupConfig
	config.AddCommand(
		newStubCommand(cmdConfigGet, "Show a value and the layer it comes from"),
		newStubCommand(cmdConfigSet, "Set a value (previews unless --apply is given)"),
		newStubCommand(contract.VerbExplain.String(), "Explain a configuration field"),
		newStubCommand(cmdConfigVal, "Check configuration against the schema"),
	)

	return []*cobra.Command{
		newStubCommand(contract.VerbPlan.String(), "Show the resolved setup plan"),
		newStubCommand(contract.VerbExplain.String(), "Explain a unit or a configuration field"),
		newStubCommand(contract.VerbStatus.String(), "Show the recorded status of every unit"),
		newStubCommand(contract.VerbNext.String(), "Show the next action for the conductor"),
		newStubCommand(contract.VerbDoctor.String(), "Check the machine's health"),
		newStubCommand(contract.CmdSetup.String(), "Run setup by stage (previews unless --apply is given)"),
		newStubCommand(contract.VerbResume.String(), "Resume an interrupted run"),
		revert,
		newStubCommand(contract.VerbDisarm.String(), "Disarm an armed dead-man timer"),
		newStubCommand(contract.VerbAdopt.String(), "Adopt existing machine state into a unit"),
		newStubCommand(contract.VerbUnmanage.String(), "Stop managing a unit and keep its files"),
		config,
		interview,
		migrate,
	}
}

// unitVerbs returns the verbs every unit command offers, in display order.
func unitVerbs() []contract.Verb {
	return []contract.Verb{
		contract.VerbPlan, contract.VerbApply, contract.VerbVerify,
		contract.VerbRevert, contract.VerbStatus, contract.VerbExplain,
	}
}

// addUnitCommands adds `wskit UNIT VERB` for every catalog unit. A unit whose
// ID is also a top-level command (doctor) gets its verbs on that command.
func addUnitCommands(root *cobra.Command) {
	for _, entry := range catalog.Entries() {
		name := entry.ID.String()
		unit := findChild(root, name)
		if unit == nil {
			unit = newGroupCommand(name, fmt.Sprintf("The %s unit (%s)", name, entry.Domain))
			unit.GroupID = groupUnits
			root.AddCommand(unit)
		}
		for _, v := range unitVerbs() {
			unit.AddCommand(newStubCommand(v.String(), fmt.Sprintf("%s the %s unit", v, name)))
		}
	}
}

func findChild(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

func newVersionCommand(ver string, flags *sharedFlags) *cobra.Command {
	return &cobra.Command{
		Use:   cmdVersion,
		Short: "Print the wskit version",
		Args:  cobra.NoArgs,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			if flags.json {
				return &Error{Class: ClassUsage, Err: fmt.Errorf("%w: %s has no JSON form yet", ErrUsage, cmdVersion)}
			}
			if _, err := io.WriteString(cmd.OutOrStdout(), ver+"\n"); err != nil {
				return fmt.Errorf("write version: %w", err)
			}
			return nil
		}),
	}
}

func newCompletionCommand() *cobra.Command {
	completion := newGroupCommand(cmdCompletion, "Print a shell completion script")
	for _, sh := range AllShells() {
		completion.AddCommand(&cobra.Command{
			Use:   string(sh),
			Short: fmt.Sprintf("Print the %s completion script", sh),
			Args:  cobra.NoArgs,
			RunE: runE(func(cmd *cobra.Command, _ []string) error {
				return writeCompletion(cmd.Root(), sh, cmd.OutOrStdout())
			}),
		})
	}
	return completion
}

func writeCompletion(root *cobra.Command, sh Shell, w io.Writer) error {
	var err error
	switch sh {
	case ShellBash:
		err = root.GenBashCompletionV2(w, true)
	case ShellZsh:
		err = root.GenZshCompletion(w)
	case ShellFish:
		err = root.GenFishCompletion(w, true)
	default:
		err = fmt.Errorf("unknown shell %q", sh)
	}
	if err != nil {
		return fmt.Errorf("generate %s completion: %w", sh, err)
	}
	return nil
}

// newGroupCommand returns a command that holds subcommands: alone it prints
// its help, and an unknown subcommand is a usage error rather than help.
func newGroupCommand(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			if err := cmd.Help(); err != nil {
				return fmt.Errorf("write help: %w", err)
			}
			return nil
		}),
	}
}

// newStubCommand returns a command that is in the tree but not built yet; it
// refuses with exit code 2.
func newStubCommand(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			return &Error{Class: ClassNotImplemented, Err: fmt.Errorf("%s: %w", cmd.CommandPath(), ErrNotImplemented)}
		}),
	}
}

// runE gives every error a command returns a class, so only cobra's own
// parse errors reach classify untyped.
func runE(fn func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		err := fn(cmd, args)
		if err == nil {
			return nil
		}
		var e *Error
		if errors.As(err, &e) || errors.Is(err, context.Canceled) {
			return err
		}
		return &Error{Class: ClassFailed, Err: err}
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}
