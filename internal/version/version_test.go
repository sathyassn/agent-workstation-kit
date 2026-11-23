package version_test

import (
	"runtime/debug"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/version"
)

func TestFrom(t *testing.T) {
	t.Parallel()

	withVersion := func(v string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: "github.com/sathyassn/agent-workstation-kit", Version: v}}
	}
	cases := []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{name: "no build info", info: nil, ok: false, want: version.Dev},
		{name: "empty version", info: withVersion(""), ok: true, want: version.Dev},
		{name: "local build", info: withVersion("(devel)"), ok: true, want: version.Dev},
		{name: "release", info: withVersion("v0.9.0"), ok: true, want: "v0.9.0"},
		{name: "pseudo version", info: withVersion("v0.9.1-0.20261123000000-abcdef123456"), ok: true, want: "v0.9.1-0.20261123000000-abcdef123456"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := version.From(tc.info, tc.ok); got != tc.want {
				t.Fatalf("From() = %q, want %q", got, tc.want)
			}
		})
	}
}
