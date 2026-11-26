package release

import (
	"errors"
	"testing"
)

func TestFaultEnvName(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		want    string
		wantErr bool
	}{
		{"declared", "package engine\n\nconst EnvFaultKillAt = \"WSKIT_FAULT_KILL_AT\"\n", "WSKIT_FAULT_KILL_AT", false},
		{"missing", "package engine\n", "", true},
		{"not at line start", "x const EnvFaultKillAt = \"A\"\n", "", true},
		{"invalid name", "const EnvFaultKillAt = \"1A\"\n", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := faultEnvName([]byte(tt.source))
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("faultEnvName = %q, %v; want %q, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestCheckSymbols(t *testing.T) {
	const mainLine = "  1000 T main.main\n"
	tests := []struct {
		name      string
		nm        string
		wantErr   bool
		wantFault bool
	}{
		{"clean", mainLine + "  2000 T github.com/x/internal/engine.New\n", false, false},
		{"no main", "  2000 T github.com/x/internal/engine.New\n", true, false},
		{"main not text", "  1000 D main.main\n", true, false},
		{"defaultFault closure", mainLine + "  2000 T github.com/x/internal/engine.defaultFault.func1\n", true, true},
		{"parseKillAt", mainLine + "  2000 T github.com/x/internal/engine.parseKillAt\n", true, true},
		{"parseKillAt inner", mainLine + "  2000 T github.com/x/internal/engine.parseKillAt.deferwrap1\n", true, true},
		{"bare defaultFault", mainLine + "  2000 T github.com/x/internal/engine.defaultFault\n", false, false},
		{"other package", mainLine + "  2000 T github.com/x/internal/other.parseKillAt\n", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkSymbols([]byte(tt.nm), "bin")
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkSymbols error = %v, want error %v", err, tt.wantErr)
			}
			if errors.Is(err, errFaultSymbol) != tt.wantFault {
				t.Fatalf("checkSymbols error = %v, want fault symbol %v", err, tt.wantFault)
			}
		})
	}
}

func TestCheckBuildTags(t *testing.T) {
	tests := []struct {
		name      string
		info      string
		wantErr   bool
		wantFault bool
	}{
		{"no tags", `{"Settings":[{"Key":"CGO_ENABLED","Value":"0"}]}`, false, false},
		{"other tags", `{"Settings":[{"Key":"-tags","Value":"netgo,osusergo"}]}`, false, false},
		{"faultinject", `{"Settings":[{"Key":"-tags","Value":"netgo,faultinject"}]}`, true, true},
		{"prefix only", `{"Settings":[{"Key":"-tags","Value":"faultinjectx"}]}`, false, false},
		{"bad json", `{`, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkBuildTags([]byte(tt.info), "bin")
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkBuildTags error = %v, want error %v", err, tt.wantErr)
			}
			if errors.Is(err, errFaultInjection) != tt.wantFault {
				t.Fatalf("checkBuildTags error = %v, want fault injection %v", err, tt.wantFault)
			}
			if errors.Is(err, errFaultSymbol) {
				t.Fatalf("checkBuildTags error = %v must not be a symbol error", err)
			}
		})
	}
}
