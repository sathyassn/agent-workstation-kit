package contract_test

import (
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
