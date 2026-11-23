package contract_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

func TestTargetKindAndUndeclaredTargetError(t *testing.T) {
	t.Parallel()

	for _, kind := range contract.AllTargetKinds() {
		if !kind.Valid() || kind.String() == "" {
			t.Errorf("TargetKind %q should be valid and non-empty", kind)
		}
	}
	if contract.TargetKind("").Valid() || contract.TargetKind("other").Valid() {
		t.Error("invalid TargetKind reported valid")
	}

	err := &contract.UndeclaredTargetError{
		Kind:   contract.TargetPath,
		Target: "/etc/agent-workstation-kit/extra.conf",
	}
	if !errors.Is(err, contract.ErrUndeclaredTarget) {
		t.Errorf("errors.Is(%v, ErrUndeclaredTarget) = false, want true", err)
	}
	var got *contract.UndeclaredTargetError
	if !errors.As(err, &got) || got.Kind != contract.TargetPath || got.Target != err.Target {
		t.Errorf("errors.As failed: got %+v", got)
	}
	if !strings.Contains(err.Error(), err.Target) || !strings.Contains(err.Error(), string(contract.TargetPath)) {
		t.Errorf("Error() = %q, want kind and target included", err.Error())
	}
}
