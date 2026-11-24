package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

// unitListValue is a --unit flag: unit IDs from the catalog, comma
// separated or repeated, so a bad ID is a usage error at parse time.
type unitListValue struct {
	units []contract.UnitID
}

// String returns the selected units, comma separated.
func (v *unitListValue) String() string {
	names := make([]string, 0, len(v.units))
	for _, u := range v.units {
		names = append(names, u.String())
	}
	return strings.Join(names, ",")
}

// Set adds the comma-separated unit IDs in s.
func (v *unitListValue) Set(s string) error {
	for _, part := range strings.Split(s, ",") {
		var u contract.UnitID
		if err := u.UnmarshalText([]byte(part)); err != nil {
			return fmt.Errorf("unit %q: %w", part, err)
		}
		v.units = append(v.units, u)
	}
	return nil
}

// Type names the flag's value in help.
func (*unitListValue) Type() string { return "units" }

// addUnitFlag declares --unit on c, with completion of the unit IDs.
func addUnitFlag(c *cobra.Command) {
	c.Flags().Var(&unitListValue{}, flagUnit, "select units by ID, comma separated or repeated")
	// Registration fails only for a missing flag, which the line above rules out.
	_ = c.RegisterFlagCompletionFunc(flagUnit, completeUnits)
}

func completeUnits(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
	ids := contract.AllUnitIDs()
	out := make([]cobra.Completion, 0, len(ids))
	for _, u := range ids {
		out = append(out, u.String())
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
