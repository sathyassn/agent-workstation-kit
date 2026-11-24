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
