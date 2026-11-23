package registry_test

import (
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/registry"
)

type fixture struct {
	goMod, goSum, tools string
}

func loadFixture(t *testing.T) fixture {
	t.Helper()

	read := func(name string) string {
		data, err := fs.ReadFile(os.DirFS("testdata"), name)
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		return string(data)
	}
	return fixture{goMod: read("go.mod"), goSum: read("go.sum"), tools: read("tools.toml")}
}

func replaceOnce(t *testing.T, s, old, repl string) string {
	t.Helper()

	if !strings.Contains(s, old) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return strings.Replace(s, old, repl, 1)
}

func TestCheckGoModulesFixtureIsClean(t *testing.T) {
	t.Parallel()

	f := loadFixture(t)
	if errs := registry.CheckGoModules([]byte(f.goMod), []byte(f.goSum), []byte(f.tools)); len(errs) > 0 {
		t.Fatalf("clean fixture reported problems: %v", errs)
	}
}

func TestCheckGoModulesRejects(t *testing.T) {
	t.Parallel()

	otherSum := "h1:" + strings.Repeat("D", 43) + "="
	cases := []struct {
		name   string
		mutate func(t *testing.T, f *fixture)
		want   string
	}{
		{
			name: "unregistered direct module",
			mutate: func(t *testing.T, f *fixture) {
				f.goMod = replaceOnce(t, f.goMod, "require example.com/lib v1.2.3",
					"require (\n\texample.com/lib v1.2.3\n\texample.com/extra v1.0.0\n)")
				f.goSum += "example.com/extra v1.0.0 " + otherSum + "\n"
			},
			want: "direct module example.com/extra v1.0.0 has no go-library entry",
		},
		{
			name: "replace directive",
			mutate: func(t *testing.T, f *fixture) {
				f.goMod += "\nreplace example.com/lib => ../lib\n"
			},
			want: "replace directive for example.com/lib",
		},
		{
			name: "registry sum differs from go.sum",
			mutate: func(t *testing.T, f *fixture) {
				f.goSum = replaceOnce(t, f.goSum,
					"example.com/lib v1.2.3 h1:"+strings.Repeat("A", 43)+"=",
					"example.com/lib v1.2.3 "+otherSum)
			},
			want: "example.com/lib v1.2.3 sum mismatch",
		},
		{
			name: "tool sum differs from go.sum",
			mutate: func(t *testing.T, f *fixture) {
				f.tools = replaceOnce(t, f.tools, "h1:"+strings.Repeat("B", 43)+"=", otherSum)
			},
			want: "example.com/tools v0.4.0 sum mismatch",
		},
		{
			name: "missing go.sum line",
			mutate: func(t *testing.T, f *fixture) {
				f.goSum = replaceOnce(t, f.goSum, "example.com/lib v1.2.3 h1:"+strings.Repeat("A", 43)+"=\n", "")
			},
			want: "go.sum has no h1 sum for example.com/lib v1.2.3",
		},
		{
			name: "registry version differs from go.mod",
			mutate: func(t *testing.T, f *fixture) {
				f.tools = replaceOnce(t, f.tools, `version = "v1.2.3"`, `version = "v1.2.4"`)
			},
			want: "example.com/lib version mismatch",
		},
		{
			name: "malformed go.mod",
			mutate: func(t *testing.T, f *fixture) {
				f.goMod += "\nrequire example.com/broken\n"
			},
			want: "parse go.mod",
		},
		{
			name: "non-exact module version",
			mutate: func(t *testing.T, f *fixture) {
				f.goMod = replaceOnce(t, f.goMod, "example.com/lib v1.2.3", "example.com/lib v1.2")
			},
			want: `module example.com/lib has non-exact version "v1.2" as written`,
		},
		{
			name: "loose toolchain",
			mutate: func(t *testing.T, f *fixture) {
				f.goMod = replaceOnce(t, f.goMod, "go 1.26.0\n", "go 1.26.0\n\ntoolchain go1.26\n")
			},
			want: "is not an exact goX.Y.Z version",
		},
		{
			name: "unregistered tool",
			mutate: func(t *testing.T, f *fixture) {
				f.goMod += "\ntool example.com/tools/cmd/other\n"
			},
			want: "tool example.com/tools/cmd/other has no go-tool entry",
		},
		{
			name: "unknown kind",
			mutate: func(t *testing.T, f *fixture) {
				f.tools = replaceOnce(t, f.tools, `kind = "go-library"`, `kind = "binary"`)
			},
			want: `unsupported kind "binary"`,
		},
		{
			name: "malformed go.sum",
			mutate: func(t *testing.T, f *fixture) {
				f.goSum += "example.com/lib v1.2.3\n"
			},
			want: "go.sum line 5 is malformed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := loadFixture(t)
			tc.mutate(t, &f)
			errs := registry.CheckGoModules([]byte(f.goMod), []byte(f.goSum), []byte(f.tools))
			for _, err := range errs {
				if strings.Contains(err.Error(), tc.want) {
					return
				}
			}
			t.Fatalf("want an error containing %q, got %v", tc.want, errs)
		})
	}
}
