package contract_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path"
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

	schemaPath := path.Join("schema/json", name+".schema.json")
	raw, err := fs.ReadFile(wskit.SchemaFS(), schemaPath)
	if err != nil {
		t.Fatalf("read embedded schema %s: %v", schemaPath, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("unmarshal schema %s: %v", schemaPath, err)
	}

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()

	url := "https://github.com/sathyassn/agent-workstation-kit/" + schemaPath
	if err := compiler.AddResource(url, doc); err != nil {
		t.Fatalf("add resource %s: %v", url, err)
	}
	schema, err := compiler.Compile(url)
	if err != nil {
		t.Fatalf("compile schema %s: %v", schemaPath, err)
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
}
