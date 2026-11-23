package contract_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

func TestRequiredEnumsRejectNullEmptyAndWrongCase(t *testing.T) {
	t.Parallel()

	cases := map[string]func([]byte) error{
		"verb":            func(b []byte) error { var v contract.Verb; return json.Unmarshal(b, &v) },
		"outcome":         func(b []byte) error { var v contract.Outcome; return json.Unmarshal(b, &v) },
		"event kind":      func(b []byte) error { var v contract.EventKind; return json.Unmarshal(b, &v) },
		"step":            func(b []byte) error { var v contract.StepID; return json.Unmarshal(b, &v) },
		"data key":        func(b []byte) error { var v contract.DataKey; return json.Unmarshal(b, &v) },
		"form factor":     func(b []byte) error { var v contract.FormFactor; return json.Unmarshal(b, &v) },
		"topology":        func(b []byte) error { var v contract.Topology; return json.Unmarshal(b, &v) },
		"change action":   func(b []byte) error { var v contract.ChangeAction; return json.Unmarshal(b, &v) },
		"risk":            func(b []byte) error { var v contract.Risk; return json.Unmarshal(b, &v) },
		"target kind":     func(b []byte) error { var v contract.TargetKind; return json.Unmarshal(b, &v) },
		"handover status": func(b []byte) error { var v contract.HandoverStatus; return json.Unmarshal(b, &v) },
		"unit id":         func(b []byte) error { var v contract.UnitID; return json.Unmarshal(b, &v) },
		"exit code":       func(b []byte) error { var v contract.ExitCode; return json.Unmarshal(b, &v) },
	}
	for name, decode := range cases {
		for _, input := range []string{`null`, `""`, `"VERIFIED"`, `"not-a-value"`} {
			if name == "exit code" && input != "null" {
				input = `99`
			}
			t.Run(name+" "+input, func(t *testing.T) {
				t.Parallel()
				if err := decode([]byte(input)); err == nil {
					t.Fatalf("%s accepted %s", name, input)
				}
			})
		}
	}
}

func TestRequiredEnumFieldRejectsNullInsideAStruct(t *testing.T) {
	t.Parallel()

	var ev contract.Event
	err := json.Unmarshal([]byte(`{"seq":1,"run_id":"r","unit":"ssh","step":"apply","kind":null,"text":"x","data":{}}`), &ev)
	if !errors.Is(err, contract.ErrUnknownValue) {
		t.Fatalf("null event kind: err = %v, want ErrUnknownValue", err)
	}
}

func TestNullableOutcomeStaysNil(t *testing.T) {
	t.Parallel()

	var p contract.Plan
	if err := json.Unmarshal([]byte(`{"outcome":null}`), &p); err != nil {
		t.Fatalf("null optional outcome: %v", err)
	}
	if p.Outcome != nil {
		t.Fatalf("outcome = %v, want nil", *p.Outcome)
	}
}

func TestExitCodeMapKeysAreValidated(t *testing.T) {
	t.Parallel()

	var good map[contract.ExitCode]string
	if err := json.Unmarshal([]byte(`{"0":"done","130":"cancelled"}`), &good); err != nil {
		t.Fatalf("valid exit code keys: %v", err)
	}
	var bad map[contract.ExitCode]string
	if err := json.Unmarshal([]byte(`{"99":"nope"}`), &bad); !errors.Is(err, contract.ErrUnknownValue) {
		t.Fatalf("exit code key 99: err = %v, want ErrUnknownValue", err)
	}
}

func TestExplainDefaultKeepsLargeIntegersExact(t *testing.T) {
	t.Parallel()

	const big = `9007199254740993`
	var e contract.Explain
	if err := json.Unmarshal([]byte(`{"default":`+big+`}`), &e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(e.Default, []byte(big)) {
		t.Fatalf("default = %s, want %s", e.Default, big)
	}
	out, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(out, []byte(`"default":`+big)) {
		t.Fatalf("round trip lost precision: %s", out)
	}
}

func TestCLIErrorRefusesSuccessCode(t *testing.T) {
	t.Parallel()

	var e contract.CLIError
	if err := json.Unmarshal([]byte(`{"schema_version":1,"code":0,"class":"refused","message":"m","rule":null}`), &e); !errors.Is(err, contract.ErrUnknownValue) {
		t.Fatalf("code 0: err = %v, want ErrUnknownValue", err)
	}
	if err := json.Unmarshal([]byte(`{"schema_version":1,"code":2,"class":"refused","message":"m","rule":null}`), &e); err != nil {
		t.Fatalf("code 2: %v", err)
	}
}
