package contract_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

func TestEnumsValidAndString(t *testing.T) {
	t.Parallel()

	for _, id := range contract.AllUnitIDs() {
		if !id.Valid() || id.String() == "" {
			t.Errorf("UnitID %q should be valid and non-empty", id)
		}
	}
	if contract.UnitID("").Valid() || contract.UnitID("unknown").Valid() {
		t.Error("invalid UnitID reported valid")
	}

	for _, st := range contract.AllStages() {
		if !st.Valid() || st.String() == "" {
			t.Errorf("Stage %q should be valid and non-empty", st)
		}
	}
	if contract.Stage("").Valid() || contract.Stage("-").Valid() {
		t.Error("invalid Stage reported valid")
	}

	for _, sc := range contract.AllScopes() {
		if !sc.Valid() || sc.String() == "" {
			t.Errorf("Scope %q should be valid and non-empty", sc)
		}
	}
	if contract.Scope("").Valid() || contract.Scope("admin").Valid() {
		t.Error("invalid Scope reported valid")
	}

	for _, d := range contract.AllDomains() {
		if !d.Valid() || d.String() == "" {
			t.Errorf("Domain %q should be valid and non-empty", d)
		}
	}
	if contract.Domain("").Valid() || contract.Domain("other").Valid() {
		t.Error("invalid Domain reported valid")
	}

	for _, p := range contract.AllPrivileges() {
		if !p.Valid() || p.String() == "" {
			t.Errorf("Privilege %q should be valid and non-empty", p)
		}
		if !p.Scope().Valid() {
			t.Errorf("Privilege %q mapped to invalid Scope %q", p, p.Scope())
		}
	}
	if contract.Privilege("").Valid() || contract.Privilege("sudo").Valid() {
		t.Error("invalid Privilege reported valid")
	}
	if contract.Privilege("sudo").Scope() != "" {
		t.Error("invalid Privilege should map to empty Scope")
	}
	if contract.PrivilegeRoot.Scope() != contract.ScopeRoot {
		t.Errorf("PrivilegeRoot.Scope() = %q, want %q", contract.PrivilegeRoot.Scope(), contract.ScopeRoot)
	}
	if contract.PrivilegePackageOwner.Scope() != contract.ScopeUser {
		t.Errorf("PrivilegePackageOwner.Scope() = %q, want %q", contract.PrivilegePackageOwner.Scope(), contract.ScopeUser)
	}
}

func TestAllSlicesReturnFreshCopies(t *testing.T) {
	t.Parallel()

	ids := contract.AllUnitIDs()
	orig := ids[0]
	ids[0] = contract.UnitID("mutated")
	if contract.AllUnitIDs()[0] != orig {
		t.Fatal("AllUnitIDs returned shared backing slice")
	}
}

func TestFormFactorAndTopologyValidationAndDecode(t *testing.T) {
	t.Parallel()

	wantFF := []contract.FormFactor{contract.FormFactorPortable, contract.FormFactorFixed}
	gotFF := contract.AllFormFactors()
	if len(gotFF) != len(wantFF) {
		t.Fatalf("AllFormFactors() = %v, want %v", gotFF, wantFF)
	}
	for i, f := range wantFF {
		if gotFF[i] != f || !f.Valid() || f.String() == "" {
			t.Errorf("FormFactor[%d] = %q, want valid %q", i, gotFF[i], f)
		}
	}
	for _, bad := range []string{"", "local-only", "laptop", "desktop", "succeeded"} {
		if contract.FormFactor(bad).Valid() {
			t.Errorf("FormFactor(%q).Valid() = true, want false", bad)
		}
		var dst contract.FormFactor
		err := json.Unmarshal([]byte(`"`+bad+`"`), &dst)
		if !errors.Is(err, contract.ErrUnknownValue) {
			t.Errorf("json.Unmarshal FormFactor(%q) err = %v, want ErrUnknownValue", bad, err)
		}
	}

	wantTop := []contract.Topology{contract.TopologySeparate, contract.TopologySingle}
	gotTop := contract.AllTopologies()
	if len(gotTop) != len(wantTop) {
		t.Fatalf("AllTopologies() = %v, want %v", gotTop, wantTop)
	}
	for i, top := range wantTop {
		if gotTop[i] != top || !top.Valid() || top.String() == "" {
			t.Errorf("Topology[%d] = %q, want valid %q", i, gotTop[i], top)
		}
	}
	for _, bad := range []string{"", "local-only", "shared", "multi", "succeeded"} {
		if contract.Topology(bad).Valid() {
			t.Errorf("Topology(%q).Valid() = true, want false", bad)
		}
		var dst contract.Topology
		err := json.Unmarshal([]byte(`"`+bad+`"`), &dst)
		if !errors.Is(err, contract.ErrUnknownValue) {
			t.Errorf("json.Unmarshal Topology(%q) err = %v, want ErrUnknownValue", bad, err)
		}
	}

	// Inputs JSON decoding must accept closed values and reject invalid ones.
	validInputs := `{"config":{},"platform":{"os":"linux","arch":"amd64","os_version":"24.04"},"form_factor":"portable","topology":"single"}`
	var in contract.Inputs
	if err := json.Unmarshal([]byte(validInputs), &in); err != nil {
		t.Fatalf("decode valid Inputs: %v", err)
	}
	if in.FormFactor != contract.FormFactorPortable || in.Topology != contract.TopologySingle {
		t.Fatalf("decoded Inputs = %+v, want portable/single", in)
	}

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{
			name: "invalid topology local-only",
			raw:  `{"config":{},"platform":{"os":"linux","arch":"amd64","os_version":"24.04"},"form_factor":"fixed","topology":"local-only"}`,
		},
		{
			name: "invalid form factor laptop",
			raw:  `{"config":{},"platform":{"os":"linux","arch":"amd64","os_version":"24.04"},"form_factor":"laptop","topology":"separate"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var bad contract.Inputs
			if err := json.Unmarshal([]byte(tc.raw), &bad); !errors.Is(err, contract.ErrUnknownValue) {
				t.Fatalf("decode Inputs(%s) err = %v, want ErrUnknownValue", tc.name, err)
			}
		})
	}
}

func TestGeneratedUnitIDUnmarshalTextRejectsUnknown(t *testing.T) {
	t.Parallel()

	for _, id := range contract.AllUnitIDs() {
		var dst contract.UnitID
		if err := json.Unmarshal([]byte(`"`+id.String()+`"`), &dst); err != nil {
			t.Errorf("json.Unmarshal valid UnitID %q failed: %v", id, err)
		}
		if dst != id {
			t.Errorf("json.Unmarshal valid UnitID = %q, want %q", dst, id)
		}
	}

	for _, bad := range []string{"", "succeeded", "unknown-unit", "SSH"} {
		var dst contract.UnitID
		if err := json.Unmarshal([]byte(`"`+bad+`"`), &dst); err == nil {
			t.Errorf("json.Unmarshal invalid UnitID %q unexpectedly succeeded (got %q)", bad, dst)
		}
	}
}

func TestEnumDecodeRejectsUnknownValues(t *testing.T) {
	t.Parallel()

	badStrings := []string{"succeeded", ""}

	checkStringEnum := func(t *testing.T, name string, unmarshal func([]byte) error, requireSentinel bool) {
		t.Helper()
		for _, bad := range badStrings {
			raw, err := json.Marshal(bad)
			if err != nil {
				t.Fatalf("marshal %q: %v", bad, err)
			}
			err = unmarshal(raw)
			if err == nil {
				t.Errorf("%s accepted unknown value %q", name, bad)
				continue
			}
			if requireSentinel && !errors.Is(err, contract.ErrUnknownValue) {
				t.Errorf("%s error for %q = %v, want ErrUnknownValue", name, bad, err)
			}
		}
	}

	checkStringEnum(t, "Verb", func(b []byte) error { var v contract.Verb; return json.Unmarshal(b, &v) }, true)
	checkStringEnum(t, "Outcome", func(b []byte) error { var v contract.Outcome; return json.Unmarshal(b, &v) }, true)
	checkStringEnum(t, "EventKind", func(b []byte) error { var v contract.EventKind; return json.Unmarshal(b, &v) }, true)
	checkStringEnum(t, "StepID", func(b []byte) error { var v contract.StepID; return json.Unmarshal(b, &v) }, true)
	checkStringEnum(t, "DataKey", func(b []byte) error { var v contract.DataKey; return json.Unmarshal(b, &v) }, true)
	checkStringEnum(t, "FormFactor", func(b []byte) error { var v contract.FormFactor; return json.Unmarshal(b, &v) }, true)
	checkStringEnum(t, "Topology", func(b []byte) error { var v contract.Topology; return json.Unmarshal(b, &v) }, true)
	checkStringEnum(t, "ChangeAction", func(b []byte) error { var v contract.ChangeAction; return json.Unmarshal(b, &v) }, true)
	checkStringEnum(t, "Risk", func(b []byte) error { var v contract.Risk; return json.Unmarshal(b, &v) }, true)
	checkStringEnum(t, "HandoverStatus", func(b []byte) error { var v contract.HandoverStatus; return json.Unmarshal(b, &v) }, true)
	checkStringEnum(t, "UnitID", func(b []byte) error { var v contract.UnitID; return json.Unmarshal(b, &v) }, false)
	checkStringEnum(t, "Stage", func(b []byte) error { var v contract.Stage; return json.Unmarshal(b, &v) }, false)
	checkStringEnum(t, "Scope", func(b []byte) error { var v contract.Scope; return json.Unmarshal(b, &v) }, false)
	checkStringEnum(t, "Domain", func(b []byte) error { var v contract.Domain; return json.Unmarshal(b, &v) }, false)
	checkStringEnum(t, "Privilege", func(b []byte) error { var v contract.Privilege; return json.Unmarshal(b, &v) }, false)

	// DataKey must also be rejected when decoded as a JSON object map key.
	for _, badKey := range []string{"succeeded", "", "free_text_key"} {
		raw := `{"` + badKey + `":"value"}`
		var m map[contract.DataKey]string
		err := json.Unmarshal([]byte(raw), &m)
		if !errors.Is(err, contract.ErrUnknownValue) {
			t.Errorf("map[DataKey]string decode with key %q err = %v, want ErrUnknownValue", badKey, err)
		}
	}

	// ExitCode must reject unknown integer codes.
	for _, badCode := range []string{"7", "99", "-1", `""`} {
		var c contract.ExitCode
		if err := json.Unmarshal([]byte(badCode), &c); err == nil {
			t.Errorf("ExitCode accepted invalid JSON %s (got %d)", badCode, c)
		}
	}
}
