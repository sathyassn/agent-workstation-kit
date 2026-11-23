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
		"verb":             func(b []byte) error { var v contract.Verb; return json.Unmarshal(b, &v) },
		"outcome":          func(b []byte) error { var v contract.Outcome; return json.Unmarshal(b, &v) },
		"event kind":       func(b []byte) error { var v contract.EventKind; return json.Unmarshal(b, &v) },
		"step":             func(b []byte) error { var v contract.StepID; return json.Unmarshal(b, &v) },
		"data key":         func(b []byte) error { var v contract.DataKey; return json.Unmarshal(b, &v) },
		"form factor":      func(b []byte) error { var v contract.FormFactor; return json.Unmarshal(b, &v) },
		"topology":         func(b []byte) error { var v contract.Topology; return json.Unmarshal(b, &v) },
		"change action":    func(b []byte) error { var v contract.ChangeAction; return json.Unmarshal(b, &v) },
		"risk":             func(b []byte) error { var v contract.Risk; return json.Unmarshal(b, &v) },
		"target kind":      func(b []byte) error { var v contract.TargetKind; return json.Unmarshal(b, &v) },
		"handover status":  func(b []byte) error { var v contract.HandoverStatus; return json.Unmarshal(b, &v) },
		"unit id":          func(b []byte) error { var v contract.UnitID; return json.Unmarshal(b, &v) },
		"exit code":        func(b []byte) error { var v contract.ExitCode; return json.Unmarshal(b, &v) },
		"command scope":    func(b []byte) error { var v contract.CommandScope; return json.Unmarshal(b, &v) },
		"runs as":          func(b []byte) error { var v contract.RunsAs; return json.Unmarshal(b, &v) },
		"command mode":     func(b []byte) error { var v contract.CommandMode; return json.Unmarshal(b, &v) },
		"non-unit command": func(b []byte) error { var v contract.NonUnitCommand; return json.Unmarshal(b, &v) },
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

func TestExitCodeIsABareIntegerOnly(t *testing.T) {
	t.Parallel()

	var c contract.ExitCode
	if err := json.Unmarshal([]byte(`2`), &c); err != nil || c != contract.ExitRefused {
		t.Fatalf("bare 2: code %d, err %v", c, err)
	}
	for _, input := range []string{`"2"`, `"0"`, `null`, `99`, `2.5`} {
		if err := json.Unmarshal([]byte(input), &c); err == nil {
			t.Errorf("exit code accepted %s", input)
		}
	}
	// Go 1.26 decodes map keys through UnmarshalJSON as quoted strings, so an
	// ExitCode-keyed map is refused outright rather than half-validated.
	var m map[contract.ExitCode]string
	if err := json.Unmarshal([]byte(`{"0":"done"}`), &m); err == nil {
		t.Fatal("exit code map key accepted")
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
	if err := json.Unmarshal([]byte(`{"schema_version":1,"code":"2","class":"refused","message":"m","rule":null}`), &e); err == nil {
		t.Fatal(`quoted code "2" accepted`)
	}
}

func TestGeneratedAndHandWrittenDecodersWrapTheSameSentinel(t *testing.T) {
	t.Parallel()

	var u contract.UnitID
	if err := json.Unmarshal([]byte(`"not-a-unit"`), &u); !errors.Is(err, contract.ErrUnknownValue) {
		t.Fatalf("generated UnitID: err = %v, want ErrUnknownValue", err)
	}
	if err := json.Unmarshal([]byte(`null`), &u); !errors.Is(err, contract.ErrUnknownValue) {
		t.Fatalf("generated UnitID null: err = %v, want ErrUnknownValue", err)
	}
	var v contract.Verb
	if err := json.Unmarshal([]byte(`"nope"`), &v); !errors.Is(err, contract.ErrUnknownValue) {
		t.Fatalf("hand-written Verb: err = %v, want ErrUnknownValue", err)
	}
}
