package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/schema"
	"github.com/sathyassn/agent-workstation-kit/internal/version"
)

const (
	flagFleetConfig     = "fleet-config"
	flagUserConfig      = "user-config"
	flagSet             = "set"
	flagReady           = "ready"
	flagFleet           = "fleet"
	flagExpectedVersion = "expected-version"

	ruleSchemaValidation = "schema-validation"
	configJSONVersion    = 1
	kitModulePath        = "github.com/sathyassn/agent-workstation-kit"
)

type configFlags struct {
	fleetConfig string
	userConfig  string
	sets        []string
}

type configGetItem struct {
	Key        schema.Key        `json:"key"`
	Value      any               `json:"value"`
	HasValue   bool              `json:"has_value"`
	Layer      schema.Layer      `json:"layer"`
	Source     string            `json:"source,omitempty"`
	IsOverride bool              `json:"is_override"`
	Rule       schema.SafetyRule `json:"rule,omitempty"`
}

type configGetResponse struct {
	SchemaVersion int             `json:"schema_version"`
	Key           schema.Key      `json:"key,omitempty"`
	Value         any             `json:"value,omitempty"`
	HasValue      bool            `json:"has_value,omitempty"`
	Layer         schema.Layer    `json:"layer,omitempty"`
	Source        string          `json:"source,omitempty"`
	IsOverride    bool            `json:"is_override,omitempty"`
	Rule          string          `json:"rule,omitempty"`
	Values        []configGetItem `json:"values,omitempty"`
}

type configSetResponse struct {
	SchemaVersion int        `json:"schema_version"`
	Applied       bool       `json:"applied"`
	Target        string     `json:"target"`
	Key           schema.Key `json:"key"`
	OldValue      any        `json:"old_value,omitempty"`
	HadOld        bool       `json:"had_old"`
	NewValue      any        `json:"new_value"`
}

type configValidateResponse struct {
	SchemaVersion int            `json:"schema_version"`
	Valid         bool           `json:"valid"`
	Target        string         `json:"target,omitempty"`
	Ready         bool           `json:"ready"`
	Fleet         bool           `json:"fleet"`
	Issues        []schema.Issue `json:"issues"`
}

func newConfigCommand(opts Options, shared *sharedFlags) *cobra.Command {
	cfgFlags := &configFlags{}
	cfg := newGroupCommand(contract.CmdConfig.String(), "Show, change and check layered configuration")
	cfg.GroupID = groupConfig
	pf := cfg.PersistentFlags()
	pf.StringVar(&cfgFlags.fleetConfig, flagFleetConfig, "", "read fleet configuration layer from this TOML file")
	pf.StringVar(&cfgFlags.userConfig, flagUserConfig, "", "read or write user configuration layer in this TOML file")
	pf.StringArrayVar(&cfgFlags.sets, flagSet, nil, "override a configuration value as KEY=VALUE (repeatable)")

	cfg.AddCommand(
		newConfigGetCommand(opts, shared, cfgFlags),
		newConfigSetCommand(opts, shared, cfgFlags),
		newConfigExplainCommand(opts, shared, cfgFlags),
		newConfigValidateCommand(opts, shared, cfgFlags),
	)
	return cfg
}

func newConfigGetCommand(opts Options, shared *sharedFlags, cfgFlags *configFlags) *cobra.Command {
	return &cobra.Command{
		Use:   cmdConfigGet + " [KEY]",
		Short: "Show a value and the layer it comes from",
		Args:  cobra.MaximumNArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveConfigLayers(opts, shared, cfgFlags)
			if err != nil {
				return mapSchemaError(err)
			}
			if len(args) == 0 {
				return writeConfigGetAll(cmd.OutOrStdout(), shared.json, resolved)
			}
			return writeConfigGetOne(cmd.OutOrStdout(), shared.json, resolved, schema.Key(args[0]))
		}),
	}
}

func newConfigSetCommand(opts Options, shared *sharedFlags, cfgFlags *configFlags) *cobra.Command {
	return &cobra.Command{
		Use:   cmdConfigSet + " KEY VALUE",
		Short: "Set a value (previews unless --apply is given)",
		Args:  cobra.ExactArgs(2),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			rawKey, rawVal := args[0], args[1]
			targetLayer := schema.LayerMachine
			targetPath := shared.config
			if cfgFlags.userConfig != "" {
				targetLayer = schema.LayerUser
				targetPath = cfgFlags.userConfig
			}

			parsed, err := schema.ParseValueForKey(targetLayer, rawKey, rawVal)
			if err != nil {
				return mapSchemaError(err)
			}
			key := schema.Key(rawKey)
			if issues := schema.ValidateFieldValue(key, parsed); len(issues) > 0 {
				return mapSchemaError(&schema.ValidationError{Issues: issues})
			}
			if targetPath == "" {
				return &Error{
					Class: ClassUsage,
					Err:   fmt.Errorf("%w: set requires --config or --user-config", ErrUsage),
				}
			}

			doc := make(map[string]any)
			const defaultPerm = fs.FileMode(0o600)
			_, statErr := os.Lstat(targetPath)
			switch {
			case statErr == nil:
				loaded, loadErr := schema.LoadRawFile(targetPath)
				if loadErr != nil {
					return mapSchemaError(loadErr)
				}
				if dupErr := schema.CheckNoDuplicateKeys(loaded); dupErr != nil {
					return mapSchemaError(dupErr)
				}
				doc = loaded
			case errors.Is(statErr, os.ErrNotExist) && targetLayer == schema.LayerUser:
				// User override files may be created on first --apply.
			default:
				return &Error{Class: ClassFailed, Err: fmt.Errorf("stat %s: %w", targetPath, statErr)}
			}

			oldVal, hadOld := schema.SetValueInMap(doc, key, parsed)
			if err := validateConfigSetResult(opts, shared, cfgFlags, targetLayer, targetPath, doc); err != nil {
				return mapSchemaError(err)
			}

			rendered, err := schema.RenderTOML(doc)
			if err != nil {
				return &Error{Class: ClassFailed, Err: err}
			}
			if shared.apply {
				if err := schema.AtomicWriteFile(targetPath, rendered, defaultPerm); err != nil {
					return &Error{Class: ClassFailed, Err: err}
				}
			}

			safeOld := schema.RedactValue(key, oldVal)
			safeNew := schema.RedactValue(key, parsed)
			resp := configSetResponse{
				SchemaVersion: configJSONVersion,
				Applied:       shared.apply,
				Target:        targetPath,
				Key:           key,
				OldValue:      safeOld,
				HadOld:        hadOld,
				NewValue:      safeNew,
			}
			if shared.json {
				return writeJSON(cmd.OutOrStdout(), resp)
			}
			verb := "PREVIEW"
			if shared.apply {
				verb = "SET"
			}
			line := fmt.Sprintf("%s %s = %s in %s\n", verb, key, formatConfigValue(safeNew), targetPath)
			if _, err := io.WriteString(cmd.OutOrStdout(), line); err != nil {
				return fmt.Errorf("write config set output: %w", err)
			}
			return nil
		}),
	}
}

func validateConfigSetResult(
	opts Options,
	shared *sharedFlags,
	cfgFlags *configFlags,
	targetLayer schema.Layer,
	targetPath string,
	doc map[string]any,
) error {
	if targetLayer == schema.LayerMachine {
		ready := false
		if st, ok := doc[schema.KeyState.String()].(string); ok && st == schema.ProfileStateApproved.String() {
			ready = true
		}
		if issues := schema.ValidateRaw(doc, ready); len(issues) > 0 {
			return &schema.ValidationError{Issues: issues}
		}
		return nil
	}

	if issues := schema.ValidatePartialRaw(doc); len(issues) > 0 {
		return &schema.ValidationError{Issues: issues}
	}
	if shared.config == "" && cfgFlags.fleetConfig == "" {
		return nil
	}

	resOpts := schema.ResolveOptions{
		Getenv: opts.Getenv,
		User:   &schema.LayerSource{Path: targetPath, Data: doc},
	}
	if cfgFlags.fleetConfig != "" {
		data, err := schema.LoadRawFile(cfgFlags.fleetConfig)
		if err != nil {
			return fmt.Errorf("load fleet config: %w", err)
		}
		resOpts.Fleet = &schema.LayerSource{Path: cfgFlags.fleetConfig, Data: data}
	}
	if shared.config != "" {
		data, err := schema.LoadRawFile(shared.config)
		if err != nil {
			return fmt.Errorf("load machine config: %w", err)
		}
		resOpts.Machine = &schema.LayerSource{Path: shared.config, Data: data}
	}
	merged, err := schema.Resolve(resOpts)
	if err != nil {
		return fmt.Errorf("resolve merged config: %w", err)
	}
	explicit := merged.ToExplicitRawMap()
	if shared.config != "" {
		ready := false
		if st, ok := explicit[schema.KeyState.String()].(string); ok && st == schema.ProfileStateApproved.String() {
			ready = true
		}
		if issues := schema.ValidateRaw(explicit, ready); len(issues) > 0 {
			return &schema.ValidationError{Issues: issues}
		}
		return nil
	}
	if issues := schema.ValidatePartialRaw(explicit); len(issues) > 0 {
		return &schema.ValidationError{Issues: issues}
	}
	return nil
}

func newConfigExplainCommand(opts Options, shared *sharedFlags, cfgFlags *configFlags) *cobra.Command {
	return &cobra.Command{
		Use:   contract.VerbExplain.String() + " KEY",
		Short: "Explain a configuration field",
		Args:  cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveConfigLayers(opts, shared, cfgFlags)
			if err != nil {
				return mapSchemaError(err)
			}
			exp, err := resolved.Explain(schema.Key(args[0]))
			if err != nil {
				return mapSchemaError(err)
			}
			if shared.json {
				return writeJSON(cmd.OutOrStdout(), exp)
			}
			return writeConfigExplainPlain(cmd.OutOrStdout(), exp)
		}),
	}
}

func newConfigValidateCommand(opts Options, shared *sharedFlags, cfgFlags *configFlags) *cobra.Command {
	var ready bool
	var fleet bool
	var expectedVersion string

	cmd := &cobra.Command{
		Use:   cmdConfigVal + " [PROFILE]",
		Short: "Check configuration against the schema",
		Args:  cobra.MaximumNArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			target := shared.config
			if len(args) == 1 {
				if target != "" {
					return &Error{
						Class: ClassUsage,
						Err:   fmt.Errorf("%w: supply either a positional path or --config, not both", ErrUsage),
					}
				}
				target = args[0]
			}
			if fleet {
				if target == "" {
					return &Error{
						Class: ClassUsage,
						Err:   fmt.Errorf("%w: --fleet requires a fleet directory path", ErrUsage),
					}
				}
				wantVersion, verErr := resolveFleetExpectedVersion(expectedVersion, opts.Version)
				if verErr != nil {
					return verErr
				}
				issues, err := schema.ValidateFleetDir(target, wantVersion)
				if err != nil {
					return &Error{Class: ClassFailed, Err: err}
				}
				return emitValidateResult(cmd, shared.json, target, true, true, issues)
			}

			hasOverlays := cfgFlags.fleetConfig != "" || cfgFlags.userConfig != "" || len(cfgFlags.sets) > 0
			if target != "" && !hasOverlays {
				envOverrides, envErr := schema.ResolveEnvLayer(opts.Getenv)
				if envErr != nil {
					return mapSchemaError(envErr)
				}
				raw, err := schema.LoadRawFile(target)
				if err != nil {
					return NewRefusal(ruleSchemaValidation, err)
				}
				if len(envOverrides) > 0 {
					fileIssues, fileErr := schema.CheckRawFileLayer(schema.LayerMachine, raw)
					if fileErr != nil {
						return mapSchemaError(fileErr)
					}
					if len(fileIssues) > 0 {
						return emitValidateResult(cmd, shared.json, target, ready, false, fileIssues)
					}
					for _, ov := range envOverrides {
						schema.SetValueInMap(raw, ov.Key, ov.Value)
					}
				} else if err := schema.CheckRawSafetyFloor(schema.LayerMachine, raw); err != nil {
					return mapSchemaError(err)
				}
				issues := schema.ValidateRaw(raw, ready)
				return emitValidateResult(cmd, shared.json, target, ready, false, issues)
			}

			localShared := *shared
			if target != "" {
				localShared.config = target
			}
			resolved, err := resolveConfigLayers(opts, &localShared, cfgFlags)
			if err != nil {
				return mapSchemaError(err)
			}
			issues := schema.ValidateRaw(resolved.ToExplicitRawMap(), ready)
			return emitValidateResult(cmd, shared.json, target, ready, false, issues)
		}),
	}
	cmd.Flags().BoolVar(&ready, flagReady, false, "require all apply-time decisions (approved state and no unresolved ask fields)")
	cmd.Flags().BoolVar(&fleet, flagFleet, false, "validate a fleet directory as one consistent unit")
	cmd.Flags().StringVar(&expectedVersion, flagExpectedVersion, "", "expected kit.lock version when validating with --fleet (defaults to running wskit version)")
	return cmd
}

func emitValidateResult(cmd *cobra.Command, asJSON bool, target string, ready, fleet bool, issues []schema.Issue) error {
	if len(issues) > 0 {
		if !asJSON {
			for _, iss := range issues {
				if _, err := io.WriteString(cmd.ErrOrStderr(), fmt.Sprintf("ERROR %s: %s\n", iss.Path, iss.Message)); err != nil {
					return fmt.Errorf("write validation error: %w", err)
				}
			}
		}
		return NewRefusal(ruleSchemaValidation, &schema.ValidationError{Issues: issues})
	}
	if asJSON {
		return writeJSON(cmd.OutOrStdout(), configValidateResponse{
			SchemaVersion: configJSONVersion,
			Valid:         true,
			Target:        target,
			Ready:         ready,
			Fleet:         fleet,
			Issues:        []schema.Issue{},
		})
	}
	label := target
	if label == "" {
		label = "layered-config"
	}
	kind := "profile"
	if fleet {
		kind = "fleet"
	}
	if _, err := io.WriteString(cmd.OutOrStdout(), fmt.Sprintf("PASS %s %s\n", kind, label)); err != nil {
		return fmt.Errorf("write validation result: %w", err)
	}
	return nil
}

func resolveFleetExpectedVersion(explicit, runningVersion string) (string, error) {
	if v := strings.TrimSpace(explicit); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(runningVersion); v != "" && v != version.Dev {
		return v, nil
	}
	cwd, err := os.Getwd()
	if err == nil {
		if repoVer, ok := findRepoVersionFile(cwd); ok {
			return repoVer, nil
		}
	}
	return "", &Error{
		Class: ClassUsage,
		Err: fmt.Errorf(
			"%w: cannot check kit.lock pin for a %s build without a readable repository VERSION file; pass --%s",
			ErrUsage, version.Dev, flagExpectedVersion,
		),
	}
}

func findRepoVersionFile(startDir string) (string, bool) {
	dir := filepath.Clean(startDir)
	for {
		modPath := filepath.Join(dir, "go.mod")
		modBytes, err := os.ReadFile(modPath) //nolint:gosec // walking ancestors for this kit's go.mod
		if err == nil {
			if !isKitGoMod(modBytes) {
				return "", false
			}
			data, readErr := os.ReadFile(filepath.Join(dir, "VERSION")) //nolint:gosec // walking ancestors for this kit's VERSION file
			if readErr == nil {
				if v := strings.TrimSpace(string(data)); v != "" {
					return v, true
				}
			}
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func isKitGoMod(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		return len(fields) == 2 && fields[0] == "module" && fields[1] == kitModulePath
	}
	return false
}

func resolveConfigLayers(opts Options, shared *sharedFlags, cfgFlags *configFlags) (*schema.ResolvedConfig, error) {
	resOpts := schema.ResolveOptions{Getenv: opts.Getenv}
	if cfgFlags.fleetConfig != "" {
		data, err := schema.LoadRawFile(cfgFlags.fleetConfig)
		if err != nil {
			return nil, fmt.Errorf("load fleet config: %w", err)
		}
		resOpts.Fleet = &schema.LayerSource{Path: cfgFlags.fleetConfig, Data: data}
	}
	if shared.config != "" {
		data, err := schema.LoadRawFile(shared.config)
		if err != nil {
			return nil, fmt.Errorf("load machine config: %w", err)
		}
		resOpts.Machine = &schema.LayerSource{Path: shared.config, Data: data}
	}
	if cfgFlags.userConfig != "" {
		data, err := schema.LoadRawFile(cfgFlags.userConfig)
		if err != nil {
			return nil, fmt.Errorf("load user config: %w", err)
		}
		resOpts.User = &schema.LayerSource{Path: cfgFlags.userConfig, Data: data}
	}
	for _, expr := range cfgFlags.sets {
		k, v, ok := strings.Cut(expr, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, &Error{
				Class: ClassUsage,
				Err:   fmt.Errorf("%w: --set %q must be KEY=VALUE", ErrUsage, expr),
			}
		}
		resOpts.Flags = append(resOpts.Flags, schema.FlagOverride{
			Key:      strings.TrimSpace(k),
			RawValue: v,
		})
	}
	resolved, err := schema.Resolve(resOpts)
	if err != nil {
		return nil, fmt.Errorf("resolve configuration layers: %w", err)
	}
	return resolved, nil
}

func mapSchemaError(err error) error {
	var cliErr *Error
	if errors.As(err, &cliErr) {
		return cliErr
	}
	var sfErr *schema.SafetyFloorError
	if errors.As(err, &sfErr) {
		return NewRefusal(sfErr.Rule.String(), sfErr)
	}
	var valErr *schema.ValidationError
	if errors.As(err, &valErr) {
		return NewRefusal(ruleSchemaValidation, valErr)
	}
	if errors.Is(err, schema.ErrUnknownKey) || errors.Is(err, schema.ErrInvalidValue) {
		return &Error{Class: ClassUsage, Err: fmt.Errorf("%w: %w", ErrUsage, err)}
	}
	return &Error{Class: ClassFailed, Err: err}
}

func writeConfigGetOne(w io.Writer, asJSON bool, resolved *schema.ResolvedConfig, key schema.Key) error {
	if sf, ok := schema.LookupSafetyFloor(key); ok && !sf.HasRequiredValue {
		if asJSON {
			return writeJSON(w, configGetResponse{
				SchemaVersion: configJSONVersion,
				Key:           key,
				Value:         "enforced",
				HasValue:      true,
				Layer:         schema.LayerSafetyFloor,
				Rule:          sf.Rule.String(),
			})
		}
		_, err := io.WriteString(w, fmt.Sprintf("%s = enforced (layer: %s, rule: %s)\n", key, schema.LayerSafetyFloor, sf.Rule))
		if err != nil {
			return fmt.Errorf("write config get: %w", err)
		}
		return nil
	}
	if _, ok := schema.LookupField(key); !ok {
		return mapSchemaError(fmt.Errorf("%w: %q", schema.ErrUnknownKey, key))
	}
	rv, found := resolved.Get(key)
	if asJSON {
		return writeJSON(w, configGetResponse{
			SchemaVersion: configJSONVersion,
			Key:           key,
			Value:         rv.Value,
			HasValue:      found,
			Layer:         rv.Layer,
			Source:        rv.Source,
			IsOverride:    rv.IsOverride,
		})
	}
	if !found {
		_, err := io.WriteString(w, fmt.Sprintf("%s = <unset>\n", key))
		if err != nil {
			return fmt.Errorf("write config get: %w", err)
		}
		return nil
	}
	line := fmt.Sprintf("%s = %s (layer: %s", key, formatConfigValue(rv.Value), rv.Layer)
	if rv.Source != "" {
		line += ", source: " + rv.Source
	}
	line += ")\n"
	if _, err := io.WriteString(w, line); err != nil {
		return fmt.Errorf("write config get: %w", err)
	}
	return nil
}

func appendf(b *bytes.Buffer, format string, args ...any) {
	b.Write(fmt.Appendf(nil, format, args...))
}

func writeConfigGetAll(w io.Writer, asJSON bool, resolved *schema.ResolvedConfig) error {
	all := resolved.All()
	if asJSON {
		items := make([]configGetItem, len(all))
		for i, rv := range all {
			items[i] = configGetItem{
				Key:        rv.Key,
				Value:      rv.Value,
				HasValue:   true,
				Layer:      rv.Layer,
				Source:     rv.Source,
				IsOverride: rv.IsOverride,
			}
		}
		return writeJSON(w, configGetResponse{
			SchemaVersion: configJSONVersion,
			Values:        items,
		})
	}
	var b bytes.Buffer
	for _, rv := range all {
		appendf(&b, "%s = %s (layer: %s)\n", rv.Key, formatConfigValue(rv.Value), rv.Layer)
	}
	if _, err := w.Write(b.Bytes()); err != nil {
		return fmt.Errorf("write config get list: %w", err)
	}
	return nil
}

func writeConfigExplainPlain(w io.Writer, exp schema.FieldExplanation) error {
	var b bytes.Buffer
	appendf(&b, "key:          %s\n", exp.Key)
	if exp.HasValue {
		appendf(&b, "value:        %s\n", formatConfigValue(exp.Value))
		appendf(&b, "layer:        %s\n", exp.Layer)
		if exp.Source != "" {
			appendf(&b, "source:       %s\n", exp.Source)
		}
	} else {
		b.WriteString("value:        <unset>\n")
		b.WriteString("layer:        unset\n")
	}
	appendf(&b, "configurable: %t\n", exp.Configurable)
	if exp.Rule != "" {
		appendf(&b, "rule:         %s\n", exp.Rule)
	}
	if exp.HasDefault {
		appendf(&b, "default:      %s\n", formatConfigValue(exp.Default))
	}
	if exp.EnvVar != "" {
		appendf(&b, "env:          %s\n", exp.EnvVar)
	}
	if exp.Stage != "" {
		appendf(&b, "stage:        %s\n", exp.Stage)
	}
	appendf(&b, "what:         %s\n", exp.What)
	appendf(&b, "why:          %s\n", exp.Why)
	if len(exp.Options) > 0 {
		b.WriteString("options:\n")
		for _, opt := range exp.Options {
			b.WriteString("  - " + opt + "\n")
		}
	}
	if exp.Recommended != "" {
		appendf(&b, "recommended:  %s\n", exp.Recommended)
	}
	appendf(&b, "risk:         %s\n", exp.Risk)
	appendf(&b, "undo:         %s\n", exp.Undo)
	if exp.ManualEquivalent != "" {
		appendf(&b, "manual:       %s\n", exp.ManualEquivalent)
	}
	if len(exp.Layers) > 0 {
		b.WriteString("layers:\n")
		for _, entry := range exp.Layers {
			if entry.Source != "" {
				appendf(&b, "  %s: %s (%s)\n", entry.Layer, formatConfigValue(entry.Value), entry.Source)
			} else {
				appendf(&b, "  %s: %s\n", entry.Layer, formatConfigValue(entry.Value))
			}
		}
	}
	if _, err := w.Write(b.Bytes()); err != nil {
		return fmt.Errorf("write config explain: %w", err)
	}
	return nil
}

func formatConfigValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "<nil>"
	case string:
		return x
	case []string:
		return "[" + strings.Join(x, ", ") + "]"
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = fmt.Sprint(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return fmt.Sprint(v)
	}
}
