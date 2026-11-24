package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

// stageValue is the --stage flag: it accepts only a stage from the unit
// catalog, so a bad value is a usage error at parse time.
type stageValue struct {
	stage contract.Stage
}

// String returns the selected stage, or "" when none is set.
func (v *stageValue) String() string { return v.stage.String() }

// Set parses s as a contract.Stage.
func (v *stageValue) Set(s string) error {
	var st contract.Stage
	if err := st.UnmarshalText([]byte(s)); err != nil {
		return fmt.Errorf("want one of %s: %w", stageList(), err)
	}
	v.stage = st
	return nil
}

// Type names the flag's value in help.
func (*stageValue) Type() string { return flagStage }

func stageList() string {
	stages := contract.AllStages()
	names := make([]string, 0, len(stages))
	for _, s := range stages {
		names = append(names, s.String())
	}
	return strings.Join(names, ", ")
}

func completeStages(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
	stages := contract.AllStages()
	out := make([]cobra.Completion, 0, len(stages))
	for _, s := range stages {
		out = append(out, s.String())
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
