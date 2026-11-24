package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/schema"
	"github.com/sathyassn/agent-workstation-kit/internal/version"
)

func executeWithEnv(t *testing.T, env map[string]string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	err := Execute(context.Background(), args, Options{
		Stdin:   strings.NewReader(""),
		Stdout:  &out,
		Stderr:  &errOut,
		Getenv:  func(k string) string { return env[k] },
		Version: testVersion,
	})
	return result{err: err, stdout: out.String(), stderr: errOut.String()}
}

// TestConfigExplainShowsValueAndLayer covers AC-2 in plain and JSON modes
// across kit-default, fleet, machine, user, environment and flag layers.
func TestConfigExplainShowsValueAndLayer(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fleetFile := filepath.Join(dir, "fleet.toml")
	machineFile := filepath.Join(dir, "machine.toml")
	userFile := filepath.Join(dir, "user.toml")

	if err := os.WriteFile(fleetFile, []byte("[backup]\nretention_days = 45\ntarget = \"fleet-backup\"\n"), 0o600); err != nil {
		t.Fatalf("write fleet.toml: %v", err)
	}
	if err := os.WriteFile(machineFile, []byte("[backup]\nretention_days = 60\n"), 0o600); err != nil {
		t.Fatalf("write machine.toml: %v", err)
	}
	if err := os.WriteFile(userFile, []byte("[backup]\nretention_days = 90\n"), 0o600); err != nil {
		t.Fatalf("write user.toml: %v", err)
	}

	env := map[string]string{
		schema.EnvVarForKey(schema.KeyBackupRetentionDays): "120",
	}

	// Plain mode: flag overrides env -> user -> machine -> fleet -> kit-default.
	r := executeWithEnv(t, env,
		"config", "explain", "backup.retention_days",
		"--fleet-config", fleetFile,
		"--config", machineFile,
		"--user-config", userFile,
		"--set", "backup.retention_days=180",
	)
	if r.err != nil {
		t.Fatalf("config explain plain failed: err=%v stderr=%q", r.err, r.stderr)
	}
	for _, want := range []string{
		"key:          backup.retention_days",
		"value:        180",
		"layer:        flag",
		"kit-default: 30",
		"fleet: 45",
		"machine: 60",
		"user: 90",
		"env: 120",
		"flag: 180",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("config explain stdout missing %q:\n%s", want, r.stdout)
		}
	}

	// JSON mode: env wins when no --set flag is passed.
	rj := executeWithEnv(t, env,
		"config", "explain", "backup.retention_days",
		"--fleet-config", fleetFile,
		"--config", machineFile,
		"--user-config", userFile,
		"--json",
	)
	if rj.err != nil || rj.stderr != "" {
		t.Fatalf("config explain --json failed: err=%v stderr=%q", rj.err, rj.stderr)
	}
	var got schema.FieldExplanation
	if err := json.Unmarshal([]byte(rj.stdout), &got); err != nil {
		t.Fatalf("decode explain JSON: %v\n%s", err, rj.stdout)
	}
	if got.Key != schema.KeyBackupRetentionDays || got.Layer != schema.LayerEnv {
		t.Fatalf("explain JSON key=%q layer=%q, want backup.retention_days / env", got.Key, got.Layer)
	}
	if num, ok := got.Value.(float64); !ok || num != 120 {
		t.Fatalf("explain JSON value = %v (%T), want 120", got.Value, got.Value)
	}
	if len(got.Layers) != 5 {
		t.Fatalf("explain JSON layers = %d, want 5", len(got.Layers))
	}

	// Kit default layer when no higher layer sets the key.
	rDef := execute(t, "config", "explain", "maintenance.timezone", "--json")
	if rDef.err != nil {
		t.Fatalf("config explain default: %v", rDef.err)
	}
	var defExp schema.FieldExplanation
	if err := json.Unmarshal([]byte(rDef.stdout), &defExp); err != nil {
		t.Fatalf("decode default explain: %v", err)
	}
	if defExp.Layer != schema.LayerKitDefault || defExp.Value != "America/Toronto" {
		t.Fatalf("default explain = %+v", defExp)
	}
}

// TestConfigSafetyFloorRefusalCLI covers AC-3 through the CLI: attempting to
// set or override a safety-floor rule through config set, --set, env or any
// file layer exits 2 with ClassRefused and a schema-valid JSON error naming
// the exact rule.
func TestConfigSafetyFloorRefusalCLI(t *testing.T) {
	t.Parallel()

	errSchema := compileSchema(t, "error")
	dir := t.TempDir()
	badFleet := filepath.Join(dir, "bad-fleet.toml")
	badMachine := filepath.Join(dir, "bad-machine.toml")
	badUser := filepath.Join(dir, "bad-user.toml")
	if err := os.WriteFile(badFleet, []byte("[safety]\nbackups = false\n"), 0o600); err != nil {
		t.Fatalf("write bad-fleet: %v", err)
	}
	if err := os.WriteFile(badMachine, []byte("[safety]\ndead_man_timer = false\n"), 0o600); err != nil {
		t.Fatalf("write bad-machine: %v", err)
	}
	if err := os.WriteFile(badUser, []byte("[security]\ndisk_encryption_required = false\n"), 0o600); err != nil {
		t.Fatalf("write bad-user: %v", err)
	}

	cases := []struct {
		name     string
		env      map[string]string
		args     []string
		wantRule schema.SafetyRule
	}{
		{
			name:     "config set safety.backups",
			args:     []string{"config", "set", "safety.backups", "false", "--json"},
			wantRule: schema.RuleSafetyFloorBackups,
		},
		{
			name:     "config set safety.root_approval_phrase even true",
			args:     []string{"config", "set", "safety.root_approval_phrase", "true", "--json"},
			wantRule: schema.RuleSafetyFloorRootApproval,
		},
		{
			name:     "config set weakens security.remote_scope",
			args:     []string{"config", "set", "security.remote_scope", "public", "--json"},
			wantRule: schema.RuleSafetyFloorRemoteScope,
		},
		{
			name:     "flag layer --set safety.undo",
			args:     []string{"config", "explain", "backup.retention_days", "--set", "safety.undo=false", "--json"},
			wantRule: schema.RuleSafetyFloorUndo,
		},
		{
			name:     "env layer WSKIT_SAFETY_SECRET_HANDLING",
			env:      map[string]string{schema.EnvVarForKey(schema.KeySafetySecretHandling): "off"},
			args:     []string{"config", "get", "backup.retention_days", "--json"},
			wantRule: schema.RuleSafetyFloorSecretHandling,
		},
		{
			name:     "fleet layer sets safety.backups",
			args:     []string{"config", "get", "backup.retention_days", "--fleet-config", badFleet, "--json"},
			wantRule: schema.RuleSafetyFloorBackups,
		},
		{
			name:     "machine layer sets safety.dead_man_timer",
			args:     []string{"config", "explain", "backup.retention_days", "--config", badMachine, "--json"},
			wantRule: schema.RuleSafetyFloorDeadManTimer,
		},
		{
			name:     "user layer weakens security.disk_encryption_required",
			args:     []string{"config", "get", "backup.retention_days", "--user-config", badUser, "--json"},
			wantRule: schema.RuleSafetyFloorDiskEncryption,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := executeWithEnv(t, tc.env, tc.args...)
			if ExitCodeFor(r.err) != contract.ExitRefused {
				t.Fatalf("exit code = %d, want 2 (err=%v)", ExitCodeFor(r.err), r.err)
			}
			var cliErr *Error
			if !errors.As(r.err, &cliErr) || cliErr.Class != ClassRefused {
				t.Fatalf("error = %v, want ClassRefused", r.err)
			}
			if cliErr.Rule != tc.wantRule.String() {
				t.Fatalf("error rule = %q, want %q", cliErr.Rule, tc.wantRule)
			}
			if r.stderr != "" {
				t.Fatalf("JSON mode wrote to stderr: %q", r.stderr)
			}
			validateJSON(t, errSchema, []byte(r.stdout))
			var wire contract.CLIError
			if err := json.Unmarshal([]byte(r.stdout), &wire); err != nil {
				t.Fatalf("decode CLIError: %v", err)
			}
			if wire.Rule == nil || *wire.Rule != tc.wantRule.String() {
				t.Fatalf("wire rule = %v, want %q", wire.Rule, tc.wantRule)
			}
		})
	}
}

func TestConfigGetSetAndValidate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	userFile := filepath.Join(dir, "user.toml")

	// Preview by default: config set does not create or modify the file.
	rPrev := execute(t, "config", "set", "backup.retention_days", "60", "--user-config", userFile)
	if rPrev.err != nil {
		t.Fatalf("config set preview failed: err=%v stderr=%q", rPrev.err, rPrev.stderr)
	}
	if !strings.Contains(rPrev.stdout, "PREVIEW backup.retention_days = 60") {
		t.Fatalf("unexpected preview output: %q", rPrev.stdout)
	}
	if _, err := os.Stat(userFile); !os.IsNotExist(err) {
		t.Fatalf("preview created %s", userFile)
	}

	// With --apply: writes atomically and config get reads the new value from user layer.
	rApply := execute(t, "config", "set", "backup.retention_days", "60", "--user-config", userFile, "--apply", "--json")
	if rApply.err != nil {
		t.Fatalf("config set --apply failed: err=%v stderr=%q", rApply.err, rApply.stderr)
	}
	var setResp configSetResponse
	if err := json.Unmarshal([]byte(rApply.stdout), &setResp); err != nil || !setResp.Applied {
		t.Fatalf("unexpected set JSON: %q (err=%v)", rApply.stdout, err)
	}

	rGet := execute(t, "config", "get", "backup.retention_days", "--user-config", userFile)
	if rGet.err != nil || !strings.Contains(rGet.stdout, "backup.retention_days = 60 (layer: user") {
		t.Fatalf("config get after set: err=%v stdout=%q", rGet.err, rGet.stdout)
	}

	// config validate on real repo fixtures (valid ready, valid draft, invalid).
	readyPath := filepath.Join("..", "..", "testdata", "profiles", "valid", "ready", "work-linux-ready.toml")
	draftPath := filepath.Join("..", "..", "testdata", "profiles", "valid", "draft", "work-linux-draft.toml")
	invalidPath := filepath.Join("..", "..", "testdata", "profiles", "invalid", "wrong-schema-version.toml")

	rValid := execute(t, "config", "validate", readyPath, "--ready", "--json")
	if rValid.err != nil {
		t.Fatalf("config validate ready failed: err=%v stderr=%q", rValid.err, rValid.stderr)
	}
	var valResp configValidateResponse
	if err := json.Unmarshal([]byte(rValid.stdout), &valResp); err != nil || !valResp.Valid {
		t.Fatalf("unexpected validate JSON: %q (err=%v)", rValid.stdout, err)
	}

	rDraftOK := execute(t, "config", "validate", draftPath)
	if rDraftOK.err != nil || !strings.Contains(rDraftOK.stdout, "PASS profile") {
		t.Fatalf("config validate draft failed: err=%v stdout=%q stderr=%q", rDraftOK.err, rDraftOK.stdout, rDraftOK.stderr)
	}

	rDraftReady := execute(t, "config", "validate", draftPath, "--ready")
	if ExitCodeFor(rDraftReady.err) != contract.ExitRefused {
		t.Fatalf("config validate draft --ready exit = %d, want 2", ExitCodeFor(rDraftReady.err))
	}

	rInvalid := execute(t, "config", "validate", invalidPath, "--json")
	if ExitCodeFor(rInvalid.err) != contract.ExitRefused {
		t.Fatalf("config validate invalid exit = %d, want 2", ExitCodeFor(rInvalid.err))
	}
	validateJSON(t, compileSchema(t, "error"), []byte(rInvalid.stdout))
}

// TestConfigSetValidatesAndRedactsSecrets covers P1: config set runs full
// key validation (principal shape, range, cross-field) on every set path
// including --user-config, and never echoes secret-like or invalid principal
// values in preview, apply or error output.
func TestConfigSetValidatesAndRedactsSecrets(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	userFile := filepath.Join(dir, "user.toml")
	badPrefixInput := "ghp_" + "example"

	// Preview and --apply, plain and --json, must reject and never echo badPrefixInput.
	for _, tc := range []struct {
		name string
		args []string
	}{
		{
			name: "github principal secret preview plain",
			args: []string{"config", "set", "source_control.github_principal", badPrefixInput, "--user-config", userFile},
		},
		{
			name: "github principal secret apply plain",
			args: []string{"config", "set", "source_control.github_principal", badPrefixInput, "--user-config", userFile, "--apply"},
		},
		{
			name: "github principal secret preview json",
			args: []string{"config", "set", "source_control.github_principal", badPrefixInput, "--user-config", userFile, "--json"},
		},
		{
			name: "github principal URL on user config",
			args: []string{"config", "set", "source_control.github_principal", "https://example.invalid/token", "--user-config", userFile},
		},
		{
			name: "backup target secret on user config",
			args: []string{"config", "set", "backup.target", badPrefixInput, "--user-config", userFile},
		},
		{
			name: "out of range port on user config",
			args: []string{"config", "set", "remote.nomachine_port", "70000", "--user-config", userFile},
		},
		{
			name: "invalid timezone on user config",
			args: []string{"config", "set", "maintenance.timezone", "Mars/Olympus", "--user-config", userFile},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := execute(t, tc.args...)
			if ExitCodeFor(r.err) != contract.ExitRefused {
				t.Fatalf("%s: exit code = %d, want 2 (err=%v stdout=%q stderr=%q)",
					tc.name, ExitCodeFor(r.err), r.err, r.stdout, r.stderr)
			}
			if strings.Contains(r.stdout, badPrefixInput) || strings.Contains(r.stderr, badPrefixInput) || (r.err != nil && strings.Contains(r.err.Error(), badPrefixInput)) {
				t.Fatalf("%s: rejected credential-shaped input echoed! stdout=%q stderr=%q err=%v", tc.name, r.stdout, r.stderr, r.err)
			}
			if strings.Contains(r.stdout, "https://example.invalid/token") || strings.Contains(r.stderr, "https://example.invalid/token") {
				t.Fatalf("%s: invalid principal echoed! stdout=%q stderr=%q", tc.name, r.stdout, r.stderr)
			}
			if _, statErr := os.Stat(userFile); !os.IsNotExist(statErr) {
				t.Fatalf("%s: rejected config set wrote %s", tc.name, userFile)
			}
		})
	}

	// Cross-field check on --user-config when paired with --config:
	// github_identity = "none" requires github_principal = "none".
	readyBytes, err := os.ReadFile(filepath.Join("..", "..", "testdata", "profiles", "valid", "ready", "work-linux-ready.toml")) //nolint:gosec // repo fixture
	if err != nil {
		t.Fatalf("read ready profile: %v", err)
	}
	ghNoneProfile := strings.Replace(string(readyBytes), `github_identity = "app"`, `github_identity = "none"`, 1)
	ghNoneProfile = strings.Replace(ghNoneProfile, `github_principal = "acme-agent-dev"`, `github_principal = "none"`, 1)
	machineFile := filepath.Join(dir, "machine.toml")
	if err := os.WriteFile(machineFile, []byte(ghNoneProfile), 0o600); err != nil { //nolint:gosec // test temp file
		t.Fatalf("write machine.toml: %v", err)
	}
	rCross := execute(t, "config", "set", "source_control.github_principal", "acme-agent-dev", "--config", machineFile, "--user-config", userFile, "--apply")
	if ExitCodeFor(rCross.err) != contract.ExitRefused {
		t.Fatalf("cross-field set exit = %d, want 2 (err=%v)", ExitCodeFor(rCross.err), rCross.err)
	}
}

// TestConfigValidateRejectsMissingRequiredIdentityKeys covers P3: kit defaults
// must never fill missing required identity keys (such as machine.platform on
// macOS) during layered config validation.
func TestConfigValidateRejectsMissingRequiredIdentityKeys(t *testing.T) {
	t.Parallel()

	macBytes, err := os.ReadFile(filepath.Join("..", "..", "testdata", "profiles", "valid", "ready", "work-macos-ready.toml")) //nolint:gosec // repo fixture
	if err != nil {
		t.Fatalf("read macOS fixture: %v", err)
	}
	macSrc := string(macBytes)

	dir := t.TempDir()
	userOverlay := filepath.Join(dir, "user.toml")
	if err := os.WriteFile(userOverlay, []byte("[backup]\nretention_days = 45\n"), 0o600); err != nil {
		t.Fatalf("write user overlay: %v", err)
	}

	cases := []struct {
		name       string
		removeLine string
	}{
		{name: "missing machine.platform", removeLine: "platform = \"macos\"\n"},
		{name: "missing machine.os_family", removeLine: "os_family = \"macos\"\n"},
		{name: "missing accounts.agent", removeLine: "agent = \"agent-01\"\n"},
		{name: "missing profile", removeLine: "profile = \"work\"\n"},
		{name: "missing state", removeLine: "state = \"approved\"\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(macSrc, tc.removeLine) {
				t.Fatalf("fixture does not contain %q", tc.removeLine)
			}
			broken := strings.Replace(macSrc, tc.removeLine, "", 1)
			brokenPath := filepath.Join(t.TempDir(), "broken.toml")
			if err := os.WriteFile(brokenPath, []byte(broken), 0o600); err != nil { //nolint:gosec // test temp file
				t.Fatalf("write broken profile: %v", err)
			}

			rSingle := execute(t, "config", "validate", brokenPath)
			if ExitCodeFor(rSingle.err) != contract.ExitRefused {
				t.Fatalf("single-file validate (%s) exit = %d, want 2 (stdout=%q stderr=%q)",
					tc.name, ExitCodeFor(rSingle.err), rSingle.stdout, rSingle.stderr)
			}

			rLayered := execute(t, "config", "validate", brokenPath, "--user-config", userOverlay)
			if ExitCodeFor(rLayered.err) != contract.ExitRefused {
				t.Fatalf("layered validate (%s) exit = %d, want 2 (stdout=%q stderr=%q)",
					tc.name, ExitCodeFor(rLayered.err), rLayered.stdout, rLayered.stderr)
			}
		})
	}
}

// TestConfigValidateAppliesEnvLayerOnSingleFile covers P5: single-file config
// validate applies the same WSKIT_* environment layer as resolve, refusing
// safety-floor violations and invalid overrides from the environment.
func TestConfigValidateAppliesEnvLayerOnSingleFile(t *testing.T) {
	t.Parallel()

	readyPath := filepath.Join("..", "..", "testdata", "profiles", "valid", "ready", "work-linux-ready.toml")

	for _, tc := range []struct {
		name     string
		env      map[string]string
		wantRule string
	}{
		{
			name:     "WSKIT_SAFETY_BACKUPS=false",
			env:      map[string]string{schema.EnvVarForKey(schema.KeySafetyBackups): "false"},
			wantRule: schema.RuleSafetyFloorBackups.String(),
		},
		{
			name:     "WSKIT_SECURITY_DISK_ENCRYPTION_REQUIRED=false",
			env:      map[string]string{schema.EnvVarForKey(schema.KeySecurityDiskEncryptionRequired): "false"},
			wantRule: schema.RuleSafetyFloorDiskEncryption.String(),
		},
		{
			name:     "WSKIT_REMOTE_NOMACHINE_PORT=70000",
			env:      map[string]string{schema.EnvVarForKey(schema.KeyRemoteNomachinePort): "70000"},
			wantRule: ruleSchemaValidation,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := executeWithEnv(t, tc.env, "config", "validate", readyPath, "--ready", "--json")
			if ExitCodeFor(r.err) != contract.ExitRefused {
				t.Fatalf("%s: exit = %d, want 2 (err=%v stdout=%q stderr=%q)",
					tc.name, ExitCodeFor(r.err), r.err, r.stdout, r.stderr)
			}
			var cliErr *Error
			if !errors.As(r.err, &cliErr) || cliErr.Class != ClassRefused || cliErr.Rule != tc.wantRule {
				t.Fatalf("%s: error = %v, want ClassRefused rule=%q", tc.name, r.err, tc.wantRule)
			}
		})
	}
}

// TestConfigValidateFleetDefaultsToRunningVersion covers P8: config validate
// --fleet checks kit.lock against the running wskit version by default without
// requiring --expected-version.
func TestConfigValidateFleetDefaultsToRunningVersion(t *testing.T) {
	t.Parallel()

	readyBytes, err := os.ReadFile(filepath.Join("..", "..", "testdata", "profiles", "valid", "ready", "work-linux-ready.toml")) //nolint:gosec // repo fixture
	if err != nil {
		t.Fatalf("read ready fixture: %v", err)
	}

	makeFleet := func(t *testing.T, lockVer string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "machines"), 0o750); err != nil {
			t.Fatalf("mkdir machines: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "kit.lock"), []byte(lockVer+"\n"), 0o600); err != nil {
			t.Fatalf("write kit.lock: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "machines", "acme-ws-001.toml"), readyBytes, 0o600); err != nil { //nolint:gosec // test temp file
			t.Fatalf("write profile: %v", err)
		}
		return dir
	}

	// Stale kit.lock MUST be rejected without --expected-version.
	staleFleet := makeFleet(t, "0.0.1")
	rStale := execute(t, "config", "validate", "--fleet", staleFleet)
	if ExitCodeFor(rStale.err) != contract.ExitRefused {
		t.Fatalf("fleet validate stale kit.lock without --expected-version: exit=%d, want 2 (stdout=%q stderr=%q)",
			ExitCodeFor(rStale.err), rStale.stdout, rStale.stderr)
	}
	if !strings.Contains(rStale.stderr, "kit.lock") {
		t.Errorf("stale fleet stderr = %q, want kit.lock error", rStale.stderr)
	}

	// Matching running version MUST pass without --expected-version.
	okFleet := makeFleet(t, testVersion)
	rOK := execute(t, "config", "validate", "--fleet", okFleet)
	if rOK.err != nil {
		t.Fatalf("fleet validate matching running version failed: err=%v stderr=%q", rOK.err, rOK.stderr)
	}

	// Explicit --expected-version override still works.
	rOverride := execute(t, "config", "validate", "--fleet", staleFleet, "--expected-version", "0.0.1")
	if rOverride.err != nil {
		t.Fatalf("fleet validate explicit --expected-version failed: err=%v stderr=%q", rOverride.err, rOverride.stderr)
	}
}

// TestConfigSetRejectsAllTokenFamiliesAndAgentInHumans covers R1: principal
// keys must run LooksLikeSecret before returning, and a standalone user
// override cannot list the agent among accounts.humans.
func TestConfigSetRejectsAllTokenFamiliesAndAgentInHumans(t *testing.T) {
	t.Parallel()

	tokens := []struct {
		name  string
		value string
	}{
		{name: "slack xoxb", value: "xoxb-" + "example"},
		{name: "aws AKIA", value: "AKIA" + strings.Repeat("A", 16)},
		{name: "aws ASIA", value: "ASIA" + strings.Repeat("B", 16)},
		{name: "npm token", value: "npm_" + "example"},
		{name: "tailscale tskey", value: "tskey-" + "example"},
		{name: "github ghp", value: "ghp_" + "example"},
		{name: "google AIza", value: "AIza" + strings.Repeat("C", 35)},
	}

	for _, tok := range tokens {
		for _, key := range []string{
			"source_control.github_principal",
			"source_control.gitlab_principal",
			"collaboration.atlassian_principal",
		} {
			t.Run(key+"/"+tok.name, func(t *testing.T) {
				t.Parallel()
				userFile := filepath.Join(t.TempDir(), "user.toml")
				r := execute(t, "config", "set", key, tok.value, "--user-config", userFile, "--apply")
				if ExitCodeFor(r.err) != contract.ExitRefused {
					t.Fatalf("%s=%s: exit=%d, want 2 (stdout=%q stderr=%q)",
						key, tok.name, ExitCodeFor(r.err), r.stdout, r.stderr)
				}
				if strings.Contains(r.stdout, tok.value) || strings.Contains(r.stderr, tok.value) || (r.err != nil && strings.Contains(r.err.Error(), tok.value)) {
					t.Fatalf("%s=%s: token echoed in output/error", key, tok.name)
				}
				if _, err := os.Stat(userFile); !os.IsNotExist(err) {
					t.Fatalf("%s=%s: rejected token was written to %s", key, tok.name, userFile)
				}
			})
		}
	}

	// Standalone user override listing accounts.agent among accounts.humans must be refused.
	dir := t.TempDir()
	userFile := filepath.Join(dir, "user.toml")
	if err := os.WriteFile(userFile, []byte("[accounts]\nagent = \"agent-01\"\n"), 0o600); err != nil {
		t.Fatalf("write user.toml: %v", err)
	}
	rAgent := execute(t, "config", "set", "accounts.humans", "alice,agent-01", "--user-config", userFile, "--apply")
	if ExitCodeFor(rAgent.err) != contract.ExitRefused {
		t.Fatalf("standalone user override listing agent in humans: exit=%d, want 2 (stdout=%q stderr=%q)",
			ExitCodeFor(rAgent.err), rAgent.stdout, rAgent.stderr)
	}
}

// TestConfigSetAcceptsAsiaTimezonesAndLabels covers R2: Asia/Tokyo,
// Asia/Kolkata and Asia-Office are accepted without redaction and do not block
// subsequent config set calls.
func TestConfigSetAcceptsAsiaTimezonesAndLabels(t *testing.T) {
	t.Parallel()

	userFile := filepath.Join(t.TempDir(), "user.toml")
	for _, tc := range []struct {
		key string
		val string
	}{
		{key: "maintenance.timezone", val: "Asia/Tokyo"},
		{key: "maintenance.timezone", val: "Asia/Kolkata"},
		{key: "backup.target", val: "Asia-Office"},
		{key: "backup.retention_days", val: "45"},
	} {
		r := execute(t, "config", "set", tc.key, tc.val, "--user-config", userFile, "--apply")
		if r.err != nil {
			t.Fatalf("config set %s=%s failed: err=%v stderr=%q", tc.key, tc.val, r.err, r.stderr)
		}
		if !strings.Contains(r.stdout, tc.val) || strings.Contains(r.stdout, schema.RedactText) {
			t.Fatalf("config set %s=%s output unexpectedly redacted or missing value: %q", tc.key, tc.val, r.stdout)
		}
	}
}

// TestConfigValidateChecksFileLayerBeforeEnvOverlay covers R3: a valid WSKIT_*
// env override must never mask a forbidden safety-floor violation or duplicate
// quoted/nested key inside the file being validated.
func TestConfigValidateChecksFileLayerBeforeEnvOverlay(t *testing.T) {
	t.Parallel()

	readyBytes, err := os.ReadFile(filepath.Join("..", "..", "testdata", "profiles", "valid", "ready", "work-linux-ready.toml")) //nolint:gosec // repo fixture
	if err != nil {
		t.Fatalf("read ready fixture: %v", err)
	}
	readySrc := string(readyBytes)

	t.Run("env cannot hide file safety-floor violation", func(t *testing.T) {
		t.Parallel()
		broken := strings.Replace(readySrc, "disk_encryption_required = true", "disk_encryption_required = false", 1)
		p := filepath.Join(t.TempDir(), "bad-safety.toml")
		if err := os.WriteFile(p, []byte(broken), 0o600); err != nil { //nolint:gosec // test temp file
			t.Fatalf("write bad-safety.toml: %v", err)
		}
		env := map[string]string{
			schema.EnvVarForKey(schema.KeySecurityDiskEncryptionRequired): "true",
		}
		r := executeWithEnv(t, env, "config", "validate", p, "--ready", "--json")
		if ExitCodeFor(r.err) != contract.ExitRefused {
			t.Fatalf("expected exit 2 refusing file safety-floor violation despite env=true, got exit=%d err=%v stdout=%q",
				ExitCodeFor(r.err), r.err, r.stdout)
		}
		var cliErr *Error
		if !errors.As(r.err, &cliErr) || cliErr.Rule != schema.RuleSafetyFloorDiskEncryption.String() {
			t.Fatalf("error = %v, want rule %q", r.err, schema.RuleSafetyFloorDiskEncryption)
		}
	})

	t.Run("env cannot hide file quoted duplicate key", func(t *testing.T) {
		t.Parallel()
		dup := "\"backup.retention_days\" = 10\n" + readySrc
		p := filepath.Join(t.TempDir(), "dup-quoted.toml")
		if err := os.WriteFile(p, []byte(dup), 0o600); err != nil { //nolint:gosec // test temp file
			t.Fatalf("write dup-quoted.toml: %v", err)
		}
		env := map[string]string{
			schema.EnvVarForKey(schema.KeyBackupRetentionDays): "30",
		}
		r := executeWithEnv(t, env, "config", "validate", p, "--ready")
		if ExitCodeFor(r.err) != contract.ExitRefused {
			t.Fatalf("expected exit 2 refusing duplicate quoted/nested key despite env override, got exit=%d err=%v stdout=%q stderr=%q",
				ExitCodeFor(r.err), r.err, r.stdout, r.stderr)
		}
	})
}

// TestConfigValidateFleetDevBuildVersionFallback covers R5: dev builds fall
// back to reading the repository VERSION file when available, and clearly
// require --expected-version when no readable VERSION file exists.
func TestConfigValidateFleetDevBuildVersionFallback(t *testing.T) {
	readyBytes, err := os.ReadFile(filepath.Join("..", "..", "testdata", "profiles", "valid", "ready", "work-linux-ready.toml")) //nolint:gosec // repo fixture
	if err != nil {
		t.Fatalf("read ready fixture: %v", err)
	}

	makeFleet := func(t *testing.T, lockVer string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "machines"), 0o750); err != nil {
			t.Fatalf("mkdir machines: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "kit.lock"), []byte(lockVer+"\n"), 0o600); err != nil {
			t.Fatalf("write kit.lock: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "machines", "acme-ws-001.toml"), readyBytes, 0o600); err != nil { //nolint:gosec // test temp file
			t.Fatalf("write profile: %v", err)
		}
		return dir
	}

	runDev := func(t *testing.T, args ...string) result {
		t.Helper()
		var out, errOut bytes.Buffer
		err := Execute(context.Background(), args, Options{
			Stdin:   strings.NewReader(""),
			Stdout:  &out,
			Stderr:  &errOut,
			Getenv:  func(string) string { return "" },
			Version: version.Dev,
		})
		return result{err: err, stdout: out.String(), stderr: errOut.String()}
	}

	t.Run("dev build reads repository VERSION file", func(t *testing.T) {
		checkout := t.TempDir()
		if err := os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("module example.invalid/test\n"), 0o600); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}
		if err := os.WriteFile(filepath.Join(checkout, "VERSION"), []byte("0.2.0-rc.7\n"), 0o600); err != nil {
			t.Fatalf("write VERSION: %v", err)
		}
		subDir := filepath.Join(checkout, "sub", "dir")
		if err := os.MkdirAll(subDir, 0o750); err != nil {
			t.Fatalf("mkdir sub: %v", err)
		}
		t.Chdir(subDir)

		okFleet := makeFleet(t, "0.2.0-rc.7")
		rOK := runDev(t, "config", "validate", "--fleet", okFleet)
		if rOK.err != nil {
			t.Fatalf("dev build with repo VERSION failed on matching kit.lock: err=%v stderr=%q", rOK.err, rOK.stderr)
		}

		staleFleet := makeFleet(t, "0.0.1")
		rStale := runDev(t, "config", "validate", "--fleet", staleFleet)
		if ExitCodeFor(rStale.err) != contract.ExitRefused {
			t.Fatalf("dev build with repo VERSION accepted stale kit.lock: exit=%d stdout=%q", ExitCodeFor(rStale.err), rStale.stdout)
		}
		if !strings.Contains(rStale.stderr, "expected 0.2.0-rc.7") {
			t.Errorf("stale stderr = %q, want 'expected 0.2.0-rc.7'", rStale.stderr)
		}
	})

	t.Run("dev build without VERSION requires --expected-version", func(t *testing.T) {
		isolated := t.TempDir()
		t.Chdir(isolated)

		fleet := makeFleet(t, "0.2.0-rc.7")
		rMissing := runDev(t, "config", "validate", "--fleet", fleet)
		if ExitCodeFor(rMissing.err) != contract.ExitRefused {
			t.Fatalf("dev build without VERSION: exit=%d, want 2", ExitCodeFor(rMissing.err))
		}
		if !strings.Contains(rMissing.stderr, version.Dev) || !strings.Contains(rMissing.stderr, "--expected-version") {
			t.Errorf("dev build without VERSION stderr = %q, want clear message mentioning dev and --expected-version", rMissing.stderr)
		}

		rExplicit := runDev(t, "config", "validate", "--fleet", fleet, "--expected-version", "0.2.0-rc.7")
		if rExplicit.err != nil {
			t.Fatalf("dev build with explicit --expected-version failed: err=%v stderr=%q", rExplicit.err, rExplicit.stderr)
		}
	})
}
