package contract_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	wskit "github.com/sathyassn/agent-workstation-kit"
	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

func expectedSchemas() []string {
	return []string{
		"describe",
		"plan",
		"next",
		"status",
		"explain",
		"handover",
		"failure",
		"event",
		"error",
	}
}

func compileEmbeddedSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()

	entries, err := fs.ReadDir(wskit.SchemaFS(), "schema/json")
	if err != nil {
		t.Fatalf("read embedded schema/json: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".schema.json") {
			continue
		}
		schemaPath := path.Join("schema/json", entry.Name())
		raw, err := fs.ReadFile(wskit.SchemaFS(), schemaPath)
		if err != nil {
			t.Fatalf("read embedded schema %s: %v", schemaPath, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("unmarshal schema %s: %v", schemaPath, err)
		}
		url := "https://github.com/sathyassn/agent-workstation-kit/" + schemaPath
		if err := compiler.AddResource(url, doc); err != nil {
			t.Fatalf("add resource %s: %v", url, err)
		}
	}

	targetPath := path.Join("schema/json", name+".schema.json")
	targetURL := "https://github.com/sathyassn/agent-workstation-kit/" + targetPath
	schema, err := compiler.Compile(targetURL)
	if err != nil {
		t.Fatalf("compile schema %s: %v", targetPath, err)
	}
	return schema
}

func validateBytes(t *testing.T, schema *jsonschema.Schema, data []byte) error {
	t.Helper()

	val, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("unmarshal JSON instance: %v", err)
	}
	return schema.Validate(val)
}

func TestEmbeddedSchemasAndGoldenFixtures(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(wskit.SchemaFS(), "schema/json")
	if err != nil {
		t.Fatalf("read embedded schema/json: %v", err)
	}
	var embeddedNames []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".schema.json") {
			embeddedNames = append(embeddedNames, strings.TrimSuffix(e.Name(), ".schema.json"))
		}
	}
	wantNames := expectedSchemas()
	slices.Sort(embeddedNames)
	sortedWant := slices.Clone(wantNames)
	slices.Sort(sortedWant)
	if !slices.Equal(embeddedNames, sortedWant) {
		t.Fatalf("embedded schemas = %v, want %v", embeddedNames, sortedWant)
	}

	fixturesFS := os.DirFS("../../testdata/json")

	for _, name := range wantNames {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema := compileEmbeddedSchema(t, name)

			validFiles, err := fs.Glob(fixturesFS, path.Join(name, "valid", "*.json"))
			if err != nil {
				t.Fatalf("glob valid fixtures for %s: %v", name, err)
			}
			if len(validFiles) < 2 {
				t.Fatalf("%s has %d valid fixtures, want at least 2", name, len(validFiles))
			}
			for _, f := range validFiles {
				t.Run("valid/"+path.Base(f), func(t *testing.T) {
					t.Parallel()
					data, err := fs.ReadFile(fixturesFS, f)
					if err != nil {
						t.Fatalf("read fixture %s: %v", f, err)
					}
					if err := validateBytes(t, schema, data); err != nil {
						t.Errorf("valid fixture %s failed schema %s: %v", f, name, err)
					}
				})
			}

			invalidFiles, err := fs.Glob(fixturesFS, path.Join(name, "invalid", "*.json"))
			if err != nil {
				t.Fatalf("glob invalid fixtures for %s: %v", name, err)
			}
			if len(invalidFiles) < 2 {
				t.Fatalf("%s has %d invalid fixtures, want at least 2", name, len(invalidFiles))
			}
			for _, f := range invalidFiles {
				t.Run("invalid/"+path.Base(f), func(t *testing.T) {
					t.Parallel()
					data, err := fs.ReadFile(fixturesFS, f)
					if err != nil {
						t.Fatalf("read fixture %s: %v", f, err)
					}
					if err := validateBytes(t, schema, data); err == nil {
						t.Errorf("invalid fixture %s unexpectedly passed schema %s", f, name)
					}
				})
			}
		})
	}
}

func TestNextOutcomeConstraint(t *testing.T) {
	t.Parallel()

	schema := compileEmbeddedSchema(t, "next")
	fixturesFS := os.DirFS("../../testdata/json")

	// Confirm the required invalid fixture with outcome "succeeded" exists and fails.
	succeededRaw, err := fs.ReadFile(fixturesFS, "next/invalid/outcome-succeeded.json")
	if err != nil {
		t.Fatalf("read outcome-succeeded fixture: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(succeededRaw, &parsed); err != nil {
		t.Fatalf("parse outcome-succeeded fixture: %v", err)
	}
	if parsed["outcome"] != "succeeded" {
		t.Fatalf("outcome-succeeded fixture has outcome=%v, want \"succeeded\"", parsed["outcome"])
	}
	if err := validateBytes(t, schema, succeededRaw); err == nil {
		t.Fatal("next schema unexpectedly accepted outcome \"succeeded\"")
	}

	baseRaw, err := fs.ReadFile(fixturesFS, "next/valid/user-step.json")
	if err != nil {
		t.Fatalf("read base next fixture: %v", err)
	}

	// Every valid Outcome value plus null must be accepted.
	allowed := []any{nil}
	for _, o := range contract.AllOutcomes() {
		allowed = append(allowed, o.String())
	}
	for _, val := range allowed {
		var doc map[string]any
		if err := json.Unmarshal(baseRaw, &doc); err != nil {
			t.Fatalf("unmarshal base next: %v", err)
		}
		doc["outcome"] = val
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshal next with outcome %v: %v", val, err)
		}
		if err := validateBytes(t, schema, raw); err != nil {
			t.Errorf("next schema rejected allowed outcome %v: %v", val, err)
		}
	}

	// Bogus outcomes must all be rejected.
	for _, bad := range []any{"succeeded", "ok", "done", "passed", "", 1, true} {
		var doc map[string]any
		if err := json.Unmarshal(baseRaw, &doc); err != nil {
			t.Fatalf("unmarshal base next: %v", err)
		}
		doc["outcome"] = bad
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshal next with bad outcome %v: %v", bad, err)
		}
		if err := validateBytes(t, schema, raw); err == nil {
			t.Errorf("next schema unexpectedly accepted outcome %v", bad)
		}
	}
}

func TestHandoverStatusConstraint(t *testing.T) {
	t.Parallel()

	schema := compileEmbeddedSchema(t, "handover")
	fixturesFS := os.DirFS("../../testdata/json")
	baseRaw, err := fs.ReadFile(fixturesFS, "handover/valid/open-root-apply.json")
	if err != nil {
		t.Fatalf("read base handover fixture: %v", err)
	}

	for _, status := range []string{"open", "blocked", "done"} {
		var doc map[string]any
		if err := json.Unmarshal(baseRaw, &doc); err != nil {
			t.Fatalf("unmarshal base handover: %v", err)
		}
		doc["status"] = status
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshal handover status %q: %v", status, err)
		}
		if err := validateBytes(t, schema, raw); err != nil {
			t.Errorf("handover schema rejected allowed status %q: %v", status, err)
		}
	}

	for _, bad := range []any{"pending", "closed", "verified", "succeeded", "", nil} {
		var doc map[string]any
		if err := json.Unmarshal(baseRaw, &doc); err != nil {
			t.Fatalf("unmarshal base handover: %v", err)
		}
		doc["status"] = bad
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshal handover bad status %v: %v", bad, err)
		}
		if err := validateBytes(t, schema, raw); err == nil {
			t.Errorf("handover schema unexpectedly accepted status %v", bad)
		}
	}
}

func TestMarshalledGoStructsValidateAgainstSchemas(t *testing.T) {
	t.Parallel()

	const digestA = contract.Digest("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	const digestB = contract.Digest("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	verified := contract.OutcomeVerified
	ts := time.Date(2026, 11, 23, 12, 30, 0, 0, time.UTC)

	type versionedDescribe struct {
		SchemaVersion int `json:"schema_version"`
		contract.Description
	}
	type versionedPlan struct {
		SchemaVersion int `json:"schema_version"`
		contract.Plan
	}
	type versionedStatus struct {
		SchemaVersion int `json:"schema_version"`
		contract.Status
	}
	type versionedEvent struct {
		SchemaVersion int `json:"schema_version"`
		contract.Event
	}

	cases := []struct {
		name   string
		schema string
		value  any
	}{
		{
			name:   "Description",
			schema: "describe",
			value: versionedDescribe{
				SchemaVersion: 1,
				Description: contract.Description{
					Unit:           contract.UnitSSH,
					Scope:          contract.ScopeRoot,
					DependsOn:      []contract.UnitID{contract.UnitAccounts, contract.UnitFirewall},
					Capabilities:   []contract.Capability{"systemd"},
					OwnedPaths:     []string{"/etc/ssh/sshd_config.d/50-agent-workstation-kit.conf"},
					OwnedStateKeys: []contract.StateKey{"sshd.allow_users"},
					Irreversible:   []string{},
					DeclaredEnv:    []string{"WSKIT_SSH_MODE"},
				},
			},
		},
		{
			name:   "Plan_pending",
			schema: "plan",
			value: versionedPlan{
				SchemaVersion: 1,
				Plan: contract.Plan{
					ID:     contract.PlanID("plan-acme-ws-001-01"),
					Unit:   contract.UnitSSH,
					Digest: digestA,
					Changes: []contract.Change{
						{
							Target: "/etc/ssh/sshd_config.d/50-agent-workstation-kit.conf",
							Action: contract.ActionReplace,
							Before: digestA,
							After:  digestB,
							Risk:   contract.RiskHigh,
						},
					},
					NonDefaults: []contract.ConfigKey{"network.ssh.port"},
					BlockedBy:   []string{},
					CannotUndo:  []string{},
					Outcome:     nil,
				},
			},
		},
		{
			name:   "Plan_with_outcome",
			schema: "plan",
			value: versionedPlan{
				SchemaVersion: 1,
				Plan: contract.Plan{
					ID:          contract.PlanID("plan-lab-ws-01-02"),
					Unit:        contract.UnitShell,
					Digest:      digestB,
					Changes:     []contract.Change{},
					NonDefaults: []contract.ConfigKey{},
					BlockedBy:   []string{},
					CannotUndo:  []string{},
					Outcome:     &verified,
				},
			},
		},
		{
			name:   "Status_with_receipt",
			schema: "status",
			value: versionedStatus{
				SchemaVersion: 1,
				Status: contract.Status{
					Unit:    contract.UnitFirewall,
					Applied: true,
					Drift:   []string{},
					LastReceipt: &contract.Receipt{
						RunID:      contract.RunID("run-acme-ws-001-01"),
						Unit:       contract.UnitFirewall,
						PlanDigest: digestA,
						Changes: []contract.AppliedChange{
							{
								Change: contract.Change{
									Target: "/etc/ufw/user.rules",
									Action: contract.ActionReplace,
									Before: digestA,
									After:  digestB,
									Risk:   contract.RiskHigh,
								},
								Backup: contract.BackupRef("backup-001"),
							},
						},
						Time:    ts,
						Outcome: contract.OutcomeVerified,
					},
				},
			},
		},
		{
			name:   "Status_without_receipt",
			schema: "status",
			value: versionedStatus{
				SchemaVersion: 1,
				Status: contract.Status{
					Unit:    contract.UnitWorkspace,
					Applied: false,
					Drift:   []string{},
				},
			},
		},
		{
			name:   "Event_with_data",
			schema: "event",
			value: versionedEvent{
				SchemaVersion: 1,
				Event: contract.Event{
					Seq:   7,
					Time:  ts,
					RunID: contract.RunID("run-lab-ws-01-01"),
					Unit:  contract.UnitSSH,
					Step:  contract.StepApply,
					Kind:  contract.EventChange,
					Text:  "wrote /etc/ssh/sshd_config.d/50-agent-workstation-kit.conf",
					Data: map[contract.DataKey]string{
						contract.DataPath:   "/etc/ssh/sshd_config.d/50-agent-workstation-kit.conf",
						contract.DataAction: string(contract.ActionReplace),
						contract.DataDigest: string(digestB),
					},
				},
			},
		},
		{
			name:   "Event_nil_data",
			schema: "event",
			value: versionedEvent{
				SchemaVersion: 1,
				Event: contract.Event{
					Seq:   0,
					Time:  ts,
					RunID: contract.RunID("run-lab-ws-01-02"),
					Unit:  contract.UnitShell,
					Step:  contract.StepPlan,
					Kind:  contract.EventStepStart,
					Text:  "planning shell",
					Data:  nil,
				},
			},
		},
		{
			name:   "Event_progress",
			schema: "event",
			value: versionedEvent{
				SchemaVersion: 1,
				Event: contract.Event{
					Seq:   8,
					Time:  ts,
					RunID: contract.RunID("run-lab-ws-01-02"),
					Unit:  contract.UnitPackages,
					Step:  contract.StepApply,
					Kind:  contract.EventProgress,
					Text:  "installing packages",
					Data: map[contract.DataKey]string{
						contract.DataCount: "3",
					},
				},
			},
		},
		{
			name:   "Handover_open",
			schema: "handover",
			value: contract.Handover{
				SchemaVersion: 1,
				Machine:       "acme-ws-001",
				Account:       "admin-01",
				Step:          "Apply root packages",
				Command: contract.HandoverCommand{
					Argv:    []string{"sudo", "/opt/agent-workstation-kit/bin/wskit", "setup", "--stage", "packages", "--apply"},
					Display: "sudo /opt/agent-workstation-kit/bin/wskit setup --stage packages --apply --approve-plan SHORT-DIGEST",
				},
				Why:    "Root packages require approval.",
				Verify: contract.HandoverVerify{Argv: []string{"wskit", "status", "--json"}},
				Undo:   ptrString("Run sudo /opt/agent-workstation-kit/bin/wskit revert --to baseline --apply"),
				Probe:  &contract.Probe{Argv: []string{"wskit", "next", "--json"}},
				Status: contract.HandoverOpen,
			},
		},
		{
			name:   "Next_user_step",
			schema: "next",
			value: contract.Next{
				SchemaVersion: 1,
				RunID:         contract.RunID("run-acme-ws-001-01"),
				Stage:         ptrStage(contract.StageAgent),
				Unit:          ptrUnitID(contract.UnitShell),
				Owner:         ptrString("agent-01"),
				Root:          false,
				Gate:          nil,
				PreviewArgv:   []string{"wskit", "shell", "apply"},
				ApplyArgv:     []string{"wskit", "shell", "apply", "--apply"},
				Handover:      nil,
				Probe:         nil,
				LogPath:       ptrString("/var/log/agent-workstation-kit/run-acme-ws-001.jsonl"),
				BlockedBy:     []string{},
				Outcome:       nil,
			},
		},
		{
			name:   "Failure_with_backup",
			schema: "failure",
			value: contract.Failure{
				SchemaVersion: 1,
				Unit:          contract.UnitSSH,
				Step:          contract.StepVerify,
				ErrorClass:    "verify",
				Message:       "sshd -t failed",
				Suggested:     "Inspect the log and re-run wskit ssh apply.",
				Log:           "/var/log/agent-workstation-kit/run-acme-ws-001-ssh.jsonl",
				Backup:        ptrBackupRef(contract.BackupRef("backup-001")),
			},
		},
		{
			name:   "Explain_unit",
			schema: "explain",
			value: contract.Explain{
				SchemaVersion:    1,
				Subject:          "ssh",
				What:             "Hardens OpenSSH.",
				Why:              "Restricts remote logins.",
				Options:          []string{"enabled", "disabled"},
				Default:          json.RawMessage(`"enabled"`),
				Risk:             "high",
				Undo:             "Run wskit ssh revert --apply.",
				ManualEquivalent: "Edit sshd_config and reload ssh.",
				Sources: []contract.ExplainSource{
					{
						URL:          "https://man.openbsd.org/sshd_config",
						Purpose:      "OpenSSH manual",
						LastVerified: "2026-11-22",
					},
				},
			},
		},
		{
			name:   "CLIError_refused",
			schema: "error",
			value: contract.CLIError{
				SchemaVersion: 1,
				Code:          contract.ExitNotStagedRoot,
				Class:         "refused",
				Message:       "root must run from the staged binary",
				Rule:          ptrString("root-staged-binary-only"),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			schema := compileEmbeddedSchema(t, tc.schema)
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			if err := validateBytes(t, schema, raw); err != nil {
				t.Errorf("marshalled %s failed schema %s: %v\njson: %s", tc.name, tc.schema, err, raw)
			}
		})
	}
}

func TestSchemaEnumsMatchGoConstants(t *testing.T) {
	t.Parallel()

	readRawSchema := func(t *testing.T, name string) map[string]any {
		t.Helper()
		raw, err := fs.ReadFile(wskit.SchemaFS(), path.Join("schema/json", name+".schema.json"))
		if err != nil {
			t.Fatalf("read schema %s: %v", name, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse schema %s: %v", name, err)
		}
		return doc
	}

	toStrings := func(t *testing.T, v any, dropNull bool) []string {
		t.Helper()
		arr, ok := v.([]any)
		if !ok {
			t.Fatalf("expected []any enum, got %T", v)
		}
		var out []string
		for _, item := range arr {
			if item == nil && dropNull {
				continue
			}
			s, ok := item.(string)
			if !ok {
				t.Fatalf("expected string enum item, got %T (%v)", item, item)
			}
			out = append(out, s)
		}
		return out
	}

	wantUnits := make([]string, len(contract.AllUnitIDs()))
	for i, id := range contract.AllUnitIDs() {
		wantUnits[i] = id.String()
	}
	wantStages := make([]string, len(contract.AllStages()))
	for i, s := range contract.AllStages() {
		wantStages[i] = s.String()
	}
	wantOutcomes := make([]string, len(contract.AllOutcomes()))
	for i, o := range contract.AllOutcomes() {
		wantOutcomes[i] = o.String()
	}
	wantSteps := make([]string, len(contract.AllStepIDs()))
	for i, s := range contract.AllStepIDs() {
		wantSteps[i] = s.String()
	}
	wantKinds := make([]string, len(contract.AllEventKinds()))
	for i, k := range contract.AllEventKinds() {
		wantKinds[i] = k.String()
	}
	wantDataKeys := make([]string, len(contract.AllDataKeys()))
	for i, k := range contract.AllDataKeys() {
		wantDataKeys[i] = k.String()
	}

	describeDoc := readRawSchema(t, "describe")
	defs := describeDoc["$defs"].(map[string]any)
	gotUnits := toStrings(t, defs["unit_id"].(map[string]any)["enum"], false)
	if !slices.Equal(gotUnits, wantUnits) {
		t.Errorf("describe unit_id enum = %v, want %v", gotUnits, wantUnits)
	}

	nextDoc := readRawSchema(t, "next")
	nextProps := nextDoc["properties"].(map[string]any)
	if got := toStrings(t, nextProps["stage"].(map[string]any)["enum"], true); !slices.Equal(got, wantStages) {
		t.Errorf("next stage enum = %v, want %v", got, wantStages)
	}
	if got := toStrings(t, nextProps["unit"].(map[string]any)["enum"], true); !slices.Equal(got, wantUnits) {
		t.Errorf("next unit enum = %v, want %v", got, wantUnits)
	}
	if got := toStrings(t, nextProps["outcome"].(map[string]any)["enum"], true); !slices.Equal(got, wantOutcomes) {
		t.Errorf("next outcome enum = %v, want %v", got, wantOutcomes)
	}

	eventDoc := readRawSchema(t, "event")
	eventProps := eventDoc["properties"].(map[string]any)
	if got := toStrings(t, eventProps["step"].(map[string]any)["enum"], false); !slices.Equal(got, wantSteps) {
		t.Errorf("event step enum = %v, want %v", got, wantSteps)
	}
	if got := toStrings(t, eventProps["kind"].(map[string]any)["enum"], false); !slices.Equal(got, wantKinds) {
		t.Errorf("event kind enum = %v, want %v", got, wantKinds)
	}
	dataOneOf := eventProps["data"].(map[string]any)["oneOf"].([]any)
	dataObj := dataOneOf[1].(map[string]any)
	gotDataKeys := toStrings(t, dataObj["propertyNames"].(map[string]any)["enum"], false)
	if !slices.Equal(gotDataKeys, wantDataKeys) {
		t.Errorf("event data propertyNames = %v, want %v", gotDataKeys, wantDataKeys)
	}

	for _, name := range []string{"plan", "status"} {
		doc := readRawSchema(t, name)
		d := doc["$defs"].(map[string]any)
		if got := toStrings(t, d["unit_id"].(map[string]any)["enum"], false); !slices.Equal(got, wantUnits) {
			t.Errorf("%s unit_id enum = %v, want %v", name, got, wantUnits)
		}
	}

	failureDoc := readRawSchema(t, "failure")
	failureProps := failureDoc["properties"].(map[string]any)
	if got := toStrings(t, failureProps["unit"].(map[string]any)["enum"], false); !slices.Equal(got, wantUnits) {
		t.Errorf("failure unit enum = %v, want %v", got, wantUnits)
	}
	if got := toStrings(t, failureProps["step"].(map[string]any)["enum"], false); !slices.Equal(got, wantSteps) {
		t.Errorf("failure step enum = %v, want %v", got, wantSteps)
	}

	errorDoc := readRawSchema(t, "error")
	errorProps := errorDoc["properties"].(map[string]any)
	rawCodes := errorProps["code"].(map[string]any)["enum"].([]any)
	var gotCodes []int
	for _, c := range rawCodes {
		gotCodes = append(gotCodes, int(c.(float64)))
	}
	var wantCodes []int
	for _, c := range contract.AllExitCodes() {
		if c != contract.ExitDone {
			wantCodes = append(wantCodes, int(c))
		}
	}
	if !slices.Equal(gotCodes, wantCodes) {
		t.Errorf("error code enum = %v, want %v", gotCodes, wantCodes)
	}

	wantHandoverStatuses := make([]string, len(contract.AllHandoverStatuses()))
	for i, s := range contract.AllHandoverStatuses() {
		wantHandoverStatuses[i] = s.String()
	}
	handoverDoc := readRawSchema(t, "handover")
	handoverProps := handoverDoc["properties"].(map[string]any)
	if got := toStrings(t, handoverProps["status"].(map[string]any)["enum"], false); !slices.Equal(got, wantHandoverStatuses) {
		t.Errorf("handover status enum = %v, want %v", got, wantHandoverStatuses)
	}
}

func TestValidFixturesRoundTripThroughGoTypes(t *testing.T) {
	t.Parallel()

	type versionedDescribe struct {
		SchemaVersion int `json:"schema_version"`
		contract.Description
	}
	type versionedPlan struct {
		SchemaVersion int `json:"schema_version"`
		contract.Plan
	}
	type versionedStatus struct {
		SchemaVersion int `json:"schema_version"`
		contract.Status
	}
	type versionedEvent struct {
		SchemaVersion int `json:"schema_version"`
		contract.Event
	}

	constructors := map[string]func() any{
		"describe": func() any { return &versionedDescribe{} },
		"plan":     func() any { return &versionedPlan{} },
		"next":     func() any { return &contract.Next{} },
		"status":   func() any { return &versionedStatus{} },
		"explain":  func() any { return &contract.Explain{} },
		"handover": func() any { return &contract.Handover{} },
		"failure":  func() any { return &contract.Failure{} },
		"event":    func() any { return &versionedEvent{} },
		"error":    func() any { return &contract.CLIError{} },
	}

	fixturesFS := os.DirFS("../../testdata/json")

	for _, name := range expectedSchemas() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			newTarget, ok := constructors[name]
			if !ok {
				t.Fatalf("no Go type constructor registered for schema %s", name)
			}
			schema := compileEmbeddedSchema(t, name)
			validFiles, err := fs.Glob(fixturesFS, path.Join(name, "valid", "*.json"))
			if err != nil {
				t.Fatalf("glob valid fixtures for %s: %v", name, err)
			}
			if len(validFiles) == 0 {
				t.Fatalf("no valid fixtures for %s", name)
			}

			for _, f := range validFiles {
				t.Run(path.Base(f), func(t *testing.T) {
					t.Parallel()

					raw, err := fs.ReadFile(fixturesFS, f)
					if err != nil {
						t.Fatalf("read %s: %v", f, err)
					}

					dst := newTarget()
					dec := json.NewDecoder(bytes.NewReader(raw))
					dec.DisallowUnknownFields()
					if err := dec.Decode(dst); err != nil {
						t.Fatalf("decode %s into %T: %v", f, dst, err)
					}

					remarshalled, err := json.Marshal(dst)
					if err != nil {
						t.Fatalf("marshal %T from %s: %v", dst, f, err)
					}
					if err := validateBytes(t, schema, remarshalled); err != nil {
						t.Fatalf("round-tripped %s failed schema %s: %v\njson: %s", f, name, err, remarshalled)
					}

					// Numbers stay json.Number, so a value beyond float64
					// precision cannot hide a lossy round trip.
					origTree, err := decodeNumberTree(raw)
					if err != nil {
						t.Fatalf("parse orig %s: %v", f, err)
					}
					gotTree, err := decodeNumberTree(remarshalled)
					if err != nil {
						t.Fatalf("parse remarshalled %s: %v", f, err)
					}
					if !reflect.DeepEqual(origTree, gotTree) {
						t.Errorf("round-trip mismatch for %s:\ngot:  %s\nwant: %s", f, remarshalled, raw)
					}
				})
			}
		})
	}
}

func TestEventSeqBounds(t *testing.T) {
	t.Parallel()

	schema := compileEmbeddedSchema(t, "event")
	const tmpl = `{
		"schema_version": 1,
		"seq": %s,
		"time": "2026-11-23T12:20:00Z",
		"run_id": "run-lab-ws-01-20261123T122000Z",
		"unit": "shell",
		"step": "apply",
		"kind": "output",
		"text": "seq bound check",
		"data": {}
	}`

	for _, validSeq := range []string{"0", "18446744073709551615"} {
		raw := []byte(strings.Replace(tmpl, "%s", validSeq, 1))
		if err := validateBytes(t, schema, raw); err != nil {
			t.Errorf("event schema rejected valid seq %s: %v", validSeq, err)
		}
	}
	for _, invalidSeq := range []string{"-1", "18446744073709551616"} {
		raw := []byte(strings.Replace(tmpl, "%s", invalidSeq, 1))
		if err := validateBytes(t, schema, raw); err == nil {
			t.Errorf("event schema unexpectedly accepted out-of-range seq %s", invalidSeq)
		}
	}
}

func TestNextHandoverSharedSchema(t *testing.T) {
	t.Parallel()

	rawSchema, err := fs.ReadFile(wskit.SchemaFS(), "schema/json/next.schema.json")
	if err != nil {
		t.Fatalf("read next.schema.json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(rawSchema, &doc); err != nil {
		t.Fatalf("parse next.schema.json: %v", err)
	}
	if defs, ok := doc["$defs"].(map[string]any); ok {
		if _, duplicated := defs["handover_payload"]; duplicated {
			t.Errorf("next.schema.json duplicates handover_payload instead of referencing handover.schema.json")
		}
	}
	props := doc["properties"].(map[string]any)
	handoverOneOf := props["handover"].(map[string]any)["oneOf"].([]any)
	foundRef := false
	for _, branch := range handoverOneOf {
		if m, ok := branch.(map[string]any); ok && m["$ref"] == "handover.schema.json" {
			foundRef = true
		}
	}
	if !foundRef {
		t.Errorf("next.schema.json properties.handover must $ref handover.schema.json, got %v", handoverOneOf)
	}

	// The nested handover inside next/valid/root-gate-handover.json must be
	// valid under both next.schema.json and handover.schema.json.
	nextRaw, err := fs.ReadFile(os.DirFS("../../testdata/json"), "next/valid/root-gate-handover.json")
	if err != nil {
		t.Fatalf("read root-gate-handover.json: %v", err)
	}
	nextSchema := compileEmbeddedSchema(t, "next")
	if err := validateBytes(t, nextSchema, nextRaw); err != nil {
		t.Fatalf("root-gate-handover.json failed next.schema.json: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(nextRaw, &parsed); err != nil {
		t.Fatalf("parse root-gate-handover.json: %v", err)
	}
	nestedRaw, err := json.Marshal(parsed["handover"])
	if err != nil {
		t.Fatalf("marshal nested handover: %v", err)
	}
	handoverSchema := compileEmbeddedSchema(t, "handover")
	if err := validateBytes(t, handoverSchema, nestedRaw); err != nil {
		t.Errorf("nested handover in root-gate-handover.json failed handover.schema.json: %v", err)
	}
}

func TestNextRejectsProbeIntervalSeconds(t *testing.T) {
	t.Parallel()

	nextSchema := compileEmbeddedSchema(t, "next")
	handoverSchema := compileEmbeddedSchema(t, "handover")
	fixturesFS := os.DirFS("../../testdata/json")

	// Reject interval_seconds in next.probe.
	baseNextRaw, err := fs.ReadFile(fixturesFS, "next/valid/user-step.json")
	if err != nil {
		t.Fatalf("read user-step.json: %v", err)
	}
	var nextDoc map[string]any
	if err := json.Unmarshal(baseNextRaw, &nextDoc); err != nil {
		t.Fatalf("parse user-step.json: %v", err)
	}
	nextDoc["probe"] = map[string]any{
		"argv":             []string{"wskit", "next", "--json"},
		"interval_seconds": 10,
	}
	badNext, err := json.Marshal(nextDoc)
	if err != nil {
		t.Fatalf("marshal bad next: %v", err)
	}
	if err := validateBytes(t, nextSchema, badNext); err == nil {
		t.Errorf("next.schema.json unexpectedly accepted probe.interval_seconds")
	}

	// Reject interval_seconds in handover.probe too.
	baseHandoverRaw, err := fs.ReadFile(fixturesFS, "handover/valid/open-root-apply.json")
	if err != nil {
		t.Fatalf("read open-root-apply.json: %v", err)
	}
	var handoverDoc map[string]any
	if err := json.Unmarshal(baseHandoverRaw, &handoverDoc); err != nil {
		t.Fatalf("parse open-root-apply.json: %v", err)
	}
	handoverDoc["probe"] = map[string]any{
		"argv":             []string{"wskit", "next", "--json"},
		"interval_seconds": 15,
	}
	badHandover, err := json.Marshal(handoverDoc)
	if err != nil {
		t.Fatalf("marshal bad handover: %v", err)
	}
	if err := validateBytes(t, handoverSchema, badHandover); err == nil {
		t.Errorf("handover.schema.json unexpectedly accepted probe.interval_seconds")
	}
}

func ptrString(s string) *string                            { return &s }
func ptrStage(s contract.Stage) *contract.Stage             { return &s }
func ptrUnitID(u contract.UnitID) *contract.UnitID          { return &u }
func ptrBackupRef(b contract.BackupRef) *contract.BackupRef { return &b }

func decodeNumberTree(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return tree, nil
}
