package schema_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/schema"
)

func TestLayerPrecedenceAndProvenance(t *testing.T) {
	t.Parallel()

	fleetData := map[string]any{
		"backup": map[string]any{
			"retention_days": int64(45),
			"target":         "fleet-backup",
		},
		"remote": map[string]any{
			"nomachine_port": int64(4001),
		},
	}
	machineData := map[string]any{
		"backup": map[string]any{
			"retention_days": int64(60),
		},
		"remote": map[string]any{
			"nomachine_port": int64(4002),
		},
	}
	userData := map[string]any{
		"backup": map[string]any{
			"retention_days": int64(90),
		},
	}
	envMap := map[string]string{
		schema.EnvVarForKey(schema.KeyBackupRetentionDays): "120",
		schema.EnvVarForKey(schema.KeyRemoteKVM):           "installed",
	}
	flags := []schema.FlagOverride{
		{Key: schema.KeyBackupRetentionDays.String(), RawValue: "180"},
	}

	resolved, err := schema.Resolve(schema.ResolveOptions{
		Fleet:   &schema.LayerSource{Path: "/fleet/values.toml", Data: fleetData},
		Machine: &schema.LayerSource{Path: "/fleet/machines/acme-ws-001.toml", Data: machineData},
		User:    &schema.LayerSource{Path: "/user/config.toml", Data: userData},
		Getenv:  func(k string) string { return envMap[k] },
		Flags:   flags,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	checks := []struct {
		key        schema.Key
		wantVal    any
		wantLayer  schema.Layer
		wantHistN  int
		wantOverrd bool
	}{
		// Kit default only.
		{key: schema.KeyMaintenanceTimezone, wantVal: "America/Toronto", wantLayer: schema.LayerKitDefault, wantHistN: 1, wantOverrd: false},
		// Fleet wins over kit default.
		{key: schema.KeyBackupTarget, wantVal: "fleet-backup", wantLayer: schema.LayerFleet, wantHistN: 2, wantOverrd: true},
		// Machine wins over fleet and kit default.
		{key: schema.KeyRemoteNomachinePort, wantVal: int64(4002), wantLayer: schema.LayerMachine, wantHistN: 3, wantOverrd: true},
		// Env wins over kit default.
		{key: schema.KeyRemoteKVM, wantVal: "installed", wantLayer: schema.LayerEnv, wantHistN: 2, wantOverrd: true},
		// Flag wins over all five lower layers.
		{key: schema.KeyBackupRetentionDays, wantVal: int64(180), wantLayer: schema.LayerFlag, wantHistN: 6, wantOverrd: true},
	}

	for _, tc := range checks {
		rv, ok := resolved.Get(tc.key)
		if !ok {
			t.Fatalf("Get(%s) missing", tc.key)
		}
		if rv.Value != tc.wantVal {
			t.Errorf("%s value = %v (%T), want %v (%T)", tc.key, rv.Value, rv.Value, tc.wantVal, tc.wantVal)
		}
		if rv.Layer != tc.wantLayer {
			t.Errorf("%s layer = %s, want %s", tc.key, rv.Layer, tc.wantLayer)
		}
		if rv.IsOverride != tc.wantOverrd {
			t.Errorf("%s IsOverride = %t, want %t", tc.key, rv.IsOverride, tc.wantOverrd)
		}
		if len(rv.History) != tc.wantHistN {
			t.Errorf("%s history len = %d, want %d (%+v)", tc.key, len(rv.History), tc.wantHistN, rv.History)
		}
	}

	// Verify Explain returns effective value, winning layer, shadowed history and metadata.
	exp, err := resolved.Explain(schema.KeyBackupRetentionDays)
	if err != nil {
		t.Fatalf("Explain(backup.retention_days): %v", err)
	}
	if !exp.Configurable || !exp.HasValue || exp.Value != int64(180) || exp.Layer != schema.LayerFlag {
		t.Fatalf("unexpected Explain result: %+v", exp)
	}
	if len(exp.Layers) != 6 {
		t.Fatalf("Explain layers = %d, want 6", len(exp.Layers))
	}
	wantOrder := schema.AllConfigurableLayers()
	for i, wantLayer := range wantOrder {
		if exp.Layers[i].Layer != wantLayer {
			t.Errorf("history[%d] layer = %s, want %s", i, exp.Layers[i].Layer, wantLayer)
		}
	}

	// Safety floor explain reports non-configurable rule.
	sfExp, err := resolved.Explain(schema.KeySafetyBackups)
	if err != nil {
		t.Fatalf("Explain(safety.backups): %v", err)
	}
	if sfExp.Configurable || sfExp.Layer != schema.LayerSafetyFloor || sfExp.Rule != schema.RuleSafetyFloorBackups {
		t.Fatalf("unexpected safety-floor Explain: %+v", sfExp)
	}
}

// TestSafetyFloorCannotBeSetThroughAnyLayer covers AC-3: every safety-floor
// rule is refused across every one of the 6 layers (kit-default, fleet,
// machine, user, env, flag) with *schema.SafetyFloorError naming the exact rule.
func TestSafetyFloorCannotBeSetThroughAnyLayer(t *testing.T) {
	t.Parallel()

	type badCase struct {
		key      string
		val      any
		rawVal   string
		wantRule schema.SafetyRule
	}

	badCases := []badCase{
		// Non-configurable safety.* keys are refused even when set to "true".
		{key: schema.KeySafetyBackups.String(), val: false, rawVal: "false", wantRule: schema.RuleSafetyFloorBackups},
		{key: schema.KeySafetyBackups.String(), val: true, rawVal: "true", wantRule: schema.RuleSafetyFloorBackups},
		{key: schema.KeySafetyUndo.String(), val: false, rawVal: "false", wantRule: schema.RuleSafetyFloorUndo},
		{key: schema.KeySafetyDeadManTimer.String(), val: false, rawVal: "false", wantRule: schema.RuleSafetyFloorDeadManTimer},
		{key: schema.KeySafetySecretHandling.String(), val: "off", rawVal: "off", wantRule: schema.RuleSafetyFloorSecretHandling},
		{key: schema.KeySafetyRootApprovalPhrase.String(), val: false, rawVal: "false", wantRule: schema.RuleSafetyFloorRootApproval},
		{key: schema.KeySafetyStagedRootBinary.String(), val: false, rawVal: "false", wantRule: schema.RuleSafetyFloorStagedRoot},
		// Profile security invariants cannot be weakened in any layer.
		{key: schema.KeySecurityDiskEncryptionRequired.String(), val: false, rawVal: "false", wantRule: schema.RuleSafetyFloorDiskEncryption},
		{key: schema.KeySecuritySecureBootRequired.String(), val: false, rawVal: "false", wantRule: schema.RuleSafetyFloorSecureBoot},
		{key: schema.KeySecurityRemoteScope.String(), val: "public", rawVal: "public", wantRule: schema.RuleSafetyFloorRemoteScope},
		{key: schema.KeyToolingInstallAgents.String(), val: false, rawVal: "false", wantRule: schema.RuleSafetyFloorRequiredAgents},
	}

	makeNested := func(dotted string, val any) map[string]any {
		sec, name, _ := strings.Cut(dotted, ".")
		return map[string]any{sec: map[string]any{name: val}}
	}

	for _, bc := range badCases {
		for _, layer := range schema.AllConfigurableLayers() {
			t.Run(string(layer)+"/"+bc.key+"="+bc.rawVal, func(t *testing.T) {
				t.Parallel()

				var opts schema.ResolveOptions
				switch layer {
				case schema.LayerKitDefault:
					opts.KitOverrides = makeNested(bc.key, bc.val)
				case schema.LayerFleet:
					opts.Fleet = &schema.LayerSource{Path: "fleet.toml", Data: makeNested(bc.key, bc.val)}
				case schema.LayerMachine:
					opts.Machine = &schema.LayerSource{Path: "machine.toml", Data: makeNested(bc.key, bc.val)}
				case schema.LayerUser:
					opts.User = &schema.LayerSource{Path: "user.toml", Data: makeNested(bc.key, bc.val)}
				case schema.LayerEnv:
					envName := schema.EnvVarForKey(schema.Key(bc.key))
					opts.Getenv = func(k string) string {
						if k == envName {
							return bc.rawVal
						}
						return ""
					}
				case schema.LayerFlag:
					opts.Flags = []schema.FlagOverride{{Key: bc.key, RawValue: bc.rawVal}}
				}

				_, err := schema.Resolve(opts)
				if err == nil {
					t.Fatalf("Resolve(%s, %s=%s) unexpectedly succeeded", layer, bc.key, bc.rawVal)
				}
				if !errors.Is(err, schema.ErrSafetyFloor) {
					t.Fatalf("Resolve error = %v, want ErrSafetyFloor", err)
				}
				var sfErr *schema.SafetyFloorError
				if !errors.As(err, &sfErr) {
					t.Fatalf("Resolve error type = %T, want *SafetyFloorError", err)
				}
				if sfErr.Rule != bc.wantRule {
					t.Errorf("SafetyFloorError.Rule = %q, want %q", sfErr.Rule, bc.wantRule)
				}
				if sfErr.Layer != layer {
					t.Errorf("SafetyFloorError.Layer = %q, want %q", sfErr.Layer, layer)
				}
				if !strings.Contains(sfErr.Error(), string(bc.wantRule)) {
					t.Errorf("SafetyFloorError.Error() = %q, must name rule %q", sfErr.Error(), bc.wantRule)
				}
			})
		}
	}

	// Undeclared safety.* key is also refused.
	_, err := schema.Resolve(schema.ResolveOptions{
		Flags: []schema.FlagOverride{{Key: "safety.custom_bypass", RawValue: "true"}},
	})
	var sfErr *schema.SafetyFloorError
	if !errors.As(err, &sfErr) || sfErr.Rule != schema.RuleSafetyFloorUnknown {
		t.Fatalf("unknown safety.* key error = %v, want RuleSafetyFloorUnknown", err)
	}
}

func TestDecodeProfileAndAtomicWrite(t *testing.T) {
	t.Parallel()

	repoRoot := repoRootDir(t)
	data, err := os.ReadFile(filepath.Join(repoRoot, "testdata", "profiles", "valid", "ready", "work-linux-ready.toml")) //nolint:gosec // repo fixture
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	p, err := schema.DecodeProfile(data, true)
	if err != nil {
		t.Fatalf("DecodeProfile: %v", err)
	}
	if p.SchemaVersion != schema.SchemaVersion || p.Machine.Hostname != "acme-ws-001" || p.Machine.Platform != schema.PlatformLinux {
		t.Fatalf("unexpected decoded profile: %+v", p)
	}

	// Invalid profile fails closed with ValidationError.
	badData, err := os.ReadFile(filepath.Join(repoRoot, "testdata", "profiles", "invalid", "wrong-schema-version.toml")) //nolint:gosec // repo fixture
	if err != nil {
		t.Fatalf("read invalid fixture: %v", err)
	}
	_, err = schema.DecodeProfile(badData, false)
	if !errors.Is(err, schema.ErrValidation) {
		t.Fatalf("DecodeProfile(invalid) = %v, want ErrValidation", err)
	}

	// AtomicWriteFile writes cleanly and leaves no temp files.
	dir := t.TempDir()
	dst := filepath.Join(dir, "profile.toml")
	if err := schema.AtomicWriteFile(dst, data, 0o600); err != nil {
		t.Fatalf("AtomicWriteFile: %v", err)
	}
	written, err := os.ReadFile(dst) //nolint:gosec // temp file
	if err != nil || string(written) != string(data) {
		t.Fatalf("AtomicWriteFile content mismatch: err=%v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("unexpected temp files in %s: %v", dir, entries)
	}
}
