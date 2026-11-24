package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/spf13/cobra"

	wskit "github.com/sathyassn/agent-workstation-kit"
	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/units/catalog"
)

const (
	testVersion = "v1.0.0-test"
	// envUpdateGolden rewrites the golden files instead of comparing.
	envUpdateGolden = "WSKIT_UPDATE_GOLDEN"
)

type result struct {
	err    error
	stdout string
	stderr string
}

// execute runs the CLI with no terminal and an empty environment.
func execute(t *testing.T, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	err := Execute(context.Background(), args, Options{
		Stdin:   strings.NewReader(""),
		Stdout:  &out,
		Stderr:  &errOut,
		Getenv:  func(string) string { return "" },
		Version: testVersion,
	})
	return result{err: err, stdout: out.String(), stderr: errOut.String()}
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if os.Getenv(envUpdateGolden) == "1" {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatalf("create testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // fixed test path
	if err != nil {
		t.Fatalf("read golden %s (set %s=1 to create it): %v", path, envUpdateGolden, err)
	}
	if got != string(want) {
		t.Fatalf("output differs from %s (set %s=1 to update)\n--- got ---\n%s", path, envUpdateGolden, got)
	}
}

// TestGoldenOutput covers AC-1: help, version and the bash, zsh and fish
// completions are generated and stable.
func TestGoldenOutput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		golden string
		args   []string
	}{
		{golden: "help", args: []string{"--help"}},
		{golden: "help-setup", args: []string{"setup", "--help"}},
		{golden: "version", args: []string{cmdVersion}},
		{golden: "completion-bash", args: []string{cmdCompletion, string(ShellBash)}},
		{golden: "completion-zsh", args: []string{cmdCompletion, string(ShellZsh)}},
		{golden: "completion-fish", args: []string{cmdCompletion, string(ShellFish)}},
	}
	for _, tc := range cases {
		t.Run(tc.golden, func(t *testing.T) {
			t.Parallel()
			r := execute(t, tc.args...)
			if r.err != nil || r.stderr != "" {
				t.Fatalf("wskit %v: err=%v stderr=%q", tc.args, r.err, r.stderr)
			}
			checkGolden(t, tc.golden, r.stdout)
		})
	}
}

// TestCompletionCoversEveryShell keeps the golden table and AllShells in step.
func TestCompletionCoversEveryShell(t *testing.T) {
	t.Parallel()
	root := NewRootCommand(Options{Version: testVersion})
	completion := findChild(root, cmdCompletion)
	if completion == nil {
		t.Fatal("no completion command")
	}
	for _, sh := range AllShells() {
		if findChild(completion, string(sh)) == nil {
			t.Errorf("completion has no %s subcommand", sh)
		}
	}
}

// TestExitCodesMatchSPC001 covers AC-2 for the codes themselves.
func TestExitCodesMatchSPC001(t *testing.T) {
	t.Parallel()
	spec := map[contract.ExitCode]int{
		contract.ExitDone:           0,
		contract.ExitFailed:         1,
		contract.ExitRefused:        2,
		contract.ExitPersonMustAct:  3,
		contract.ExitRecoveryBlocks: 4,
		contract.ExitNotStagedRoot:  5,
		contract.ExitDriftFound:     6,
		contract.ExitCancelled:      130,
	}
	for code, want := range spec {
		if int(code) != want {
			t.Errorf("exit code constant %d, want %d", code, want)
		}
	}

	cases := []struct {
		name string
		err  error
		want contract.ExitCode
	}{
		{name: "nil", err: nil, want: contract.ExitDone},
		{name: "untyped", err: errors.New("boom"), want: contract.ExitFailed},
		{name: "context cancelled", err: context.Canceled, want: contract.ExitCancelled},
		{name: "failed", err: NewError(ClassFailed, errors.New("x")), want: contract.ExitFailed},
		{name: "usage", err: NewError(ClassUsage, ErrUsage), want: contract.ExitRefused},
		{name: "refused", err: NewRefusal("rule", errors.New("x")), want: contract.ExitRefused},
		{name: "not implemented", err: NewError(ClassNotImplemented, ErrNotImplemented), want: contract.ExitRefused},
		{name: "person must act", err: NewError(ClassPersonMustAct, errors.New("x")), want: contract.ExitPersonMustAct},
		{name: "recovery", err: NewError(ClassRecovery, errors.New("x")), want: contract.ExitRecoveryBlocks},
		{name: "not staged root", err: NewError(ClassNotStagedRoot, errors.New("x")), want: contract.ExitNotStagedRoot},
		{name: "drift", err: NewError(ClassDrift, errors.New("x")), want: contract.ExitDriftFound},
		{name: "cancelled", err: NewError(ClassCancelled, context.Canceled), want: contract.ExitCancelled},
		{name: "wrapped drift", err: errors.Join(errors.New("ctx"), NewError(ClassDrift, errors.New("x"))), want: contract.ExitDriftFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ExitCodeFor(tc.err); got != tc.want {
				t.Fatalf("ExitCodeFor = %d, want %d", got, tc.want)
			}
		})
	}

	// Every failure code has a class, and every class has a failure code.
	reached := map[contract.ExitCode]bool{contract.ExitDone: true}
	for _, c := range AllClasses() {
		code := c.ExitCode()
		if !code.Valid() || code == contract.ExitDone {
			t.Errorf("class %s maps to %d, want a failure code", c, code)
		}
		reached[code] = true
	}
	for _, code := range contract.AllExitCodes() {
		if !reached[code] {
			t.Errorf("no class maps to exit code %d", code)
		}
	}
}

// TestExecuteExitCodes covers AC-2 through the command line.
func TestExecuteExitCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		args  []string
		code  contract.ExitCode
		class Class
	}{
		{name: "no args shows help", args: nil, code: contract.ExitDone},
		{name: "help", args: []string{"--help"}, code: contract.ExitDone},
		{name: "version", args: []string{cmdVersion}, code: contract.ExitDone},
		{name: "unknown command", args: []string{"bogus"}, code: contract.ExitRefused, class: ClassUsage},
		{name: "unknown flag", args: []string{contract.VerbPlan.String(), "--bogus"}, code: contract.ExitRefused, class: ClassUsage},
		{name: "unknown unit verb", args: []string{contract.UnitShell.String(), "bogus"}, code: contract.ExitRefused, class: ClassUsage},
		{name: "unknown config verb", args: []string{contract.CmdConfig.String(), "bogus"}, code: contract.ExitRefused, class: ClassUsage},
		{name: "verbose with quiet", args: []string{"--verbose", "--quiet", contract.VerbPlan.String()}, code: contract.ExitRefused, class: ClassUsage},
		{name: "version takes no args", args: []string{cmdVersion, "x"}, code: contract.ExitRefused, class: ClassUsage},
		{name: "version has no JSON form", args: []string{cmdVersion, "--json"}, code: contract.ExitRefused, class: ClassUsage},
		{name: "stub top-level", args: []string{contract.VerbNext.String()}, code: contract.ExitRefused, class: ClassNotImplemented},
		{name: "stub unit verb", args: []string{contract.UnitShell.String(), contract.VerbApply.String(), "--apply"}, code: contract.ExitRefused, class: ClassNotImplemented},
		{name: "stub doctor unit verb", args: []string{contract.VerbDoctor.String(), contract.VerbVerify.String()}, code: contract.ExitRefused, class: ClassNotImplemented},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := execute(t, tc.args...)
			if got := ExitCodeFor(r.err); got != tc.code {
				t.Fatalf("wskit %v exit code = %d, want %d (err=%v)", tc.args, got, tc.code, r.err)
			}
			if tc.class == "" {
				return
			}
			var e *Error
			if !errors.As(r.err, &e) || e.Class != tc.class {
				t.Fatalf("wskit %v error = %v, want class %s", tc.args, r.err, tc.class)
			}
			if slices.Contains(tc.args, "--"+flagJSON) {
				if !strings.HasPrefix(r.stdout, "{") || r.stderr != "" {
					t.Fatalf("JSON mode stdout=%q stderr=%q, want the error as JSON on stdout", r.stdout, r.stderr)
				}
				return
			}
			if !strings.HasPrefix(r.stderr, programName+": ") {
				t.Fatalf("plain mode stderr = %q, want one error line", r.stderr)
			}
		})
	}
}

func compileSchema(t *testing.T, name string) *jsonschema.Schema { //nolint:unparam // reusable for future CLI JSON schemas
	t.Helper()
	raw, err := fs.ReadFile(wskit.SchemaFS(), "schema/json/"+name+".schema.json")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	url := "https://example.invalid/" + name + ".schema.json"
	if err := c.AddResource(url, doc); err != nil {
		t.Fatalf("add schema: %v", err)
	}
	s, err := c.Compile(url)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return s
}

func validateJSON(t *testing.T, s *jsonschema.Schema, data []byte) {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("not JSON: %q: %v", data, err)
	}
	if err := s.Validate(inst); err != nil {
		t.Fatalf("%s fails the schema: %v", data, err)
	}
}

// TestJSONErrorsMatchSchema checks that every class, and a real JSON-mode
// failure, produce an error.schema.json document on standard output.
func TestJSONErrorsMatchSchema(t *testing.T) {
	t.Parallel()
	schema := compileSchema(t, "error")
	for _, c := range AllClasses() {
		for _, err := range []error{NewError(c, errors.New("x")), &Error{Class: c, Rule: "a-rule", Err: errors.New("x")}} {
			raw, mErr := json.Marshal(ToCLIError(err))
			if mErr != nil {
				t.Fatalf("marshal: %v", mErr)
			}
			validateJSON(t, schema, raw)
		}
	}

	r := execute(t, "--json", "bogus")
	if ExitCodeFor(r.err) != contract.ExitRefused || r.stderr != "" {
		t.Fatalf("--json bogus: err=%v stderr=%q", r.err, r.stderr)
	}
	validateJSON(t, schema, []byte(r.stdout))
	var got contract.CLIError
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("decode CLI error: %v", err)
	}
	if got.Code != contract.ExitRefused || got.Class != string(ClassUsage) {
		t.Fatalf("CLI error = %+v, want code 2 class usage", got)
	}
}

func allCommands(c *cobra.Command) []*cobra.Command {
	out := []*cobra.Command{c}
	for _, sub := range c.Commands() {
		out = append(out, allCommands(sub)...)
	}
	return out
}

// TestStageFlagAndNoScope covers AC-3.
func TestStageFlagAndNoScope(t *testing.T) {
	t.Parallel()
	root := NewRootCommand(Options{Version: testVersion})
	for _, c := range allCommands(root) {
		if c.Flags().Lookup("scope") != nil || c.InheritedFlags().Lookup("scope") != nil {
			t.Errorf("%s has a --scope flag", c.CommandPath())
		}
	}
	setup := findChild(root, contract.CmdSetup.String())
	if setup == nil || setup.InheritedFlags().Lookup(flagStage) == nil {
		t.Fatal("setup has no --stage flag")
	}

	for _, st := range contract.AllStages() {
		r := execute(t, contract.CmdSetup.String(), "--stage", st.String())
		var e *Error
		if !errors.As(r.err, &e) || e.Class != ClassNotImplemented {
			t.Errorf("setup --stage %s: err=%v, want the not-implemented stub", st, r.err)
		}
	}
	for _, args := range [][]string{
		{contract.CmdSetup.String(), "--scope", contract.StagePackages.String()},
		{contract.CmdSetup.String(), "--stage", "nonexistent"},
		{contract.CmdSetup.String(), "--stage="},
	} {
		r := execute(t, args...)
		var e *Error
		if !errors.As(r.err, &e) || e.Class != ClassUsage {
			t.Errorf("wskit %v: err=%v, want a usage error", args, r.err)
		}
	}

	r := execute(t, cobra.ShellCompRequestCmd, contract.CmdSetup.String(), "--stage", "")
	for _, st := range contract.AllStages() {
		if !strings.Contains(r.stdout, st.String()+"\n") {
			t.Errorf("--stage completion lacks %s: %q", st, r.stdout)
		}
	}
}

// TestTreeCoversMatrixAndCatalog checks that every verb and command in the
// SPC-001 command matrix is reachable, and every catalog unit has its verbs.
func TestTreeCoversMatrixAndCatalog(t *testing.T) {
	t.Parallel()
	root := NewRootCommand(Options{Version: testVersion})
	for _, row := range contract.CommandMatrix() {
		for _, nu := range row.NonUnits {
			if findChild(root, nu.String()) == nil {
				t.Errorf("matrix row %q: no %s command", row.Command, nu)
			}
		}
		for _, v := range row.Verbs {
			if findChild(root, v.String()) == nil && !slices.Contains(unitVerbs(), v) {
				t.Errorf("matrix row %q: verb %s is neither a command nor a unit verb", row.Command, v)
			}
		}
	}
	for _, entry := range catalog.Entries() {
		unit := findChild(root, entry.ID.String())
		if unit == nil {
			t.Errorf("no command for unit %s", entry.ID)
			continue
		}
		for _, v := range unitVerbs() {
			if findChild(unit, v.String()) == nil {
				t.Errorf("unit %s has no %s verb", entry.ID, v)
			}
		}
	}
}

func TestResolveOutputMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		flags          outputFlags
		env            map[string]string
		outTTY, errTTY bool
		want           OutputMode
	}{
		{name: "json wins over plain env", flags: outputFlags{json: true}, env: map[string]string{envPlain: "1"}, outTTY: true, errTTY: true, want: ModeJSON},
		{name: "json wins over plain flag", flags: outputFlags{json: true, plain: true}, want: ModeJSON},
		{name: "plain flag on a terminal", flags: outputFlags{plain: true}, outTTY: true, errTTY: true, want: ModePlain},
		{name: "plain env on a terminal", env: map[string]string{envPlain: "1"}, outTTY: true, errTTY: true, want: ModePlain},
		{name: "terminal", outTTY: true, errTTY: true, want: ModeTUI},
		{name: "dumb terminal", env: map[string]string{envTerm: termDumb}, outTTY: true, errTTY: true, want: ModePlain},
		{name: "stdout piped", errTTY: true, want: ModePlain},
		{name: "stderr piped", outTTY: true, want: ModePlain},
		{name: "no terminal", want: ModePlain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveOutputMode(tc.flags, func(k string) string { return tc.env[k] }, tc.outTTY, tc.errTTY)
			if got != tc.want {
				t.Fatalf("mode = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestOutputFlagsFollowCobraParsing checks that the output mode is read the
// way cobra parses the command line, so a flag value that looks like --json
// or --plain does not change the mode.
func TestOutputFlagsFollowCobraParsing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		args []string
		want outputFlags
	}{
		{args: []string{"plan", "--json"}, want: outputFlags{json: true}},
		{args: []string{"--plain", "status"}, want: outputFlags{plain: true}},
		{args: []string{"--json=false", "--plain=true"}, want: outputFlags{plain: true}},
		{args: []string{"version", "--config", "--json"}, want: outputFlags{}},
		{args: []string{"--config", "--plain", "plan"}, want: outputFlags{}},
		{args: []string{"revert", "--to", "--json"}, want: outputFlags{}},
		{args: []string{"explain", "--", "--json"}, want: outputFlags{}},
		{args: []string{"bogus", "--json"}, want: outputFlags{json: true}},
		{args: []string{"plan", "--bogus", "--json"}, want: outputFlags{json: true}},
		{args: []string{"plan", "--unit", "bogus", "--json"}, want: outputFlags{json: true}},
		{args: []string{"plan", "--unit=bogus", "--plain"}, want: outputFlags{plain: true}},
		{args: []string{"--stage", "bogus", "status", "--json"}, want: outputFlags{json: true}},
		{args: []string{"setup", "--unit", "bogus", "--json=false", "--plain"}, want: outputFlags{plain: true}},
	}
	for _, tc := range cases {
		if got := outputFlagsFor(Options{Version: testVersion}, tc.args); got != tc.want {
			t.Errorf("outputFlagsFor(%v) = %+v, want %+v", tc.args, got, tc.want)
		}
	}
}

// TestFlagValueDoesNotSelectJSON runs a failing command whose --config
// value is "--json": the error is a plain line on standard error, not JSON.
func TestFlagValueDoesNotSelectJSON(t *testing.T) {
	t.Parallel()
	r := execute(t, contract.VerbPlan.String(), "--config", "--json")
	if r.stdout != "" || !strings.HasPrefix(r.stderr, programName+": ") {
		t.Fatalf("wskit plan --config --json: stdout=%q stderr=%q, want a plain error line", r.stdout, r.stderr)
	}
}

// TestInvalidFlagValueKeepsOutputMode checks that a flag value that fails
// validation does not hide a later --json or --plain: the usage error comes
// out in the mode asked for.
func TestInvalidFlagValueKeepsOutputMode(t *testing.T) {
	t.Parallel()
	schema := compileSchema(t, "error")
	cases := [][]string{
		{contract.VerbPlan.String(), "--" + flagUnit, "bogus", "--" + flagJSON},
		{"--" + flagStage, "bogus", contract.VerbStatus.String(), "--" + flagJSON},
	}
	for _, args := range cases {
		r := execute(t, args...)
		if ExitCodeFor(r.err) != contract.ExitRefused || r.stderr != "" {
			t.Fatalf("wskit %v: err=%v stderr=%q, want exit 2 and nothing on standard error", args, r.err, r.stderr)
		}
		validateJSON(t, schema, []byte(r.stdout))
		var got contract.CLIError
		if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
			t.Fatalf("wskit %v: decode CLI error: %v", args, err)
		}
		if got.Class != string(ClassUsage) {
			t.Fatalf("wskit %v: CLI error = %+v, want class usage", args, got)
		}
	}

	r := execute(t, contract.VerbPlan.String(), "--"+flagUnit, "bogus", "--"+flagPlain)
	if ExitCodeFor(r.err) != contract.ExitRefused || r.stdout != "" ||
		!strings.HasPrefix(r.stderr, programName+": ") || strings.Count(r.stderr, "\n") != 1 {
		t.Fatalf("wskit plan --unit bogus --plain: stdout=%q stderr=%q, want one plain error line", r.stdout, r.stderr)
	}
}

// TestCompletionRefusesJSON checks that completion, like version, refuses
// --json with a schema-conforming error for every shell.
func TestCompletionRefusesJSON(t *testing.T) {
	t.Parallel()
	schema := compileSchema(t, "error")
	for _, sh := range AllShells() {
		t.Run(string(sh), func(t *testing.T) {
			t.Parallel()
			r := execute(t, cmdCompletion, string(sh), "--json")
			var e *Error
			if !errors.As(r.err, &e) || e.Class != ClassUsage || ExitCodeFor(r.err) != contract.ExitRefused {
				t.Fatalf("completion %s --json: err=%v, want a usage error with exit code 2", sh, r.err)
			}
			validateJSON(t, schema, []byte(r.stdout))
		})
	}
}

// TestDeclaredFlags checks the per-command flags SPC-001 and ADR 0005
// declare, and that each reaches its command's not-implemented path.
func TestDeclaredFlags(t *testing.T) {
	t.Parallel()
	root := NewRootCommand(Options{Version: testVersion})
	declared := map[string][]string{
		contract.CmdSetup.String():   {"all", "unit"},
		contract.VerbPlan.String():   {"unit"},
		contract.VerbResume.String(): {"restore-only"},
		contract.VerbRevert.String(): {"to"},
		contract.VerbDisarm.String(): {"run"},
	}
	for name, flags := range declared {
		c := findChild(root, name)
		if c == nil {
			t.Errorf("no %s command", name)
			continue
		}
		for _, f := range flags {
			if c.LocalFlags().Lookup(f) == nil {
				t.Errorf("%s has no --%s flag", name, f)
			}
		}
	}

	cases := []struct {
		args  []string
		class Class
	}{
		{args: []string{"setup", "--all"}, class: ClassNotImplemented},
		{args: []string{"setup", "--unit", "shell,vpn"}, class: ClassNotImplemented},
		{args: []string{"plan", "--unit", "shell", "--unit", "vpn"}, class: ClassNotImplemented},
		{args: []string{"resume", "--restore-only"}, class: ClassNotImplemented},
		{args: []string{"revert", "--to", "baseline"}, class: ClassNotImplemented},
		{args: []string{"disarm", "--run", "run-1"}, class: ClassNotImplemented},
		{args: []string{"setup", "--unit", "bogus"}, class: ClassUsage},
		{args: []string{"setup", "--all", "--stage", "packages"}, class: ClassUsage},
		{args: []string{"setup", "--all", "--unit", "shell"}, class: ClassUsage},
	}
	for _, tc := range cases {
		r := execute(t, tc.args...)
		var e *Error
		if !errors.As(r.err, &e) || e.Class != tc.class {
			t.Errorf("wskit %v: err=%v, want class %s", tc.args, r.err, tc.class)
		}
	}
}

func TestIsTerminalFalseWithoutTerminal(t *testing.T) {
	t.Parallel()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = devNull.Close() })
	if isTerminal(devNull) {
		t.Errorf("%s reported as a terminal", os.DevNull)
	}
	if isTerminal(&bytes.Buffer{}) {
		t.Error("a buffer reported as a terminal")
	}
}
