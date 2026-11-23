package gen_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/units/catalog/gen"
)

const minimalValidCatalog = `
schema_version = 1

[[unit]]
id = "baseline"
domain = "system"
stage = { linux = "baseline", macos = "baseline" }
privilege = { linux = "root", macos = "root" }
depends = { linux = [], macos = [] }
epic = "EPC-005"
milestone = "alpha"

[[unit]]
id = "packages-user"
domain = "system"
stage = { linux = "-", macos = "packages-user" }
privilege = { linux = "-", macos = "package-owner" }
depends = { linux = [], macos = ["baseline"] }
epic = "EPC-013"
milestone = "alpha"

[[unit]]
id = "packages"
domain = "system"
stage = { linux = "packages", macos = "packages" }
privilege = { linux = "root", macos = "root" }
depends = { linux = ["baseline"], macos = ["packages-user"] }
epic = "EPC-013"
milestone = "alpha"

[[unit]]
id = "agentctl"
domain = "system"
stage = { linux = "services", macos = "-" }
privilege = { linux = "root", macos = "-" }
depends = { linux = ["packages"], macos = [] }
epic = "EPC-019"
milestone = "alpha"

[[unit]]
id = "mesh-vpn"
domain = "network"
stage = { linux = "remote", macos = "remote" }
privilege = { linux = "root", macos = "root" }
depends = { linux = ["packages"], macos = ["packages"] }
epic = "EPC-016"
milestone = "alpha"

[[unit]]
id = "firewall"
domain = "network"
stage = { linux = "remote", macos = "remote" }
privilege = { linux = "root", macos = "root" }
depends = { linux = ["packages"], macos = ["packages"] }
depends_if = { remote-access = { linux = ["mesh-vpn"], macos = ["mesh-vpn"] } }
epic = "EPC-015"
milestone = "alpha"

[[unit]]
id = "ssh"
domain = "remote"
stage = { linux = "remote", macos = "remote" }
privilege = { linux = "root", macos = "root" }
depends = { linux = ["firewall"], macos = ["firewall"] }
depends_if = { remote-access = { linux = ["mesh-vpn"], macos = ["mesh-vpn"] } }
epic = "EPC-015"
milestone = "alpha"

[[unit]]
id = "vpn"
domain = "network"
stage = { linux = "remote", macos = "remote" }
privilege = { linux = "root", macos = "root" }
depends = { linux = ["packages"], macos = ["packages"] }
waits_for = ["s5-disarm"]
epic = "EPC-007"
milestone = "alpha"
`

func replaceOnce(t *testing.T, s, old, repl string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return strings.Replace(s, old, repl, 1)
}

func TestParseCatalogRejectsBadInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		toml string
		want string
	}{
		{
			name: "malformed toml",
			toml: "schema_version = [",
			want: "decode units catalog",
		},
		{
			name: "unknown top-level key",
			toml: "schema_version = 1\nextra = true\n",
			want: "unknown keys",
		},
		{
			name: "unsupported schema version",
			toml: "schema_version = 2\n",
			want: "schema_version = 2, want 1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := gen.ParseCatalog([]byte(tc.toml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ParseCatalog() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestValidateCleanFixture(t *testing.T) {
	t.Parallel()

	cat, err := gen.ParseCatalog([]byte(minimalValidCatalog))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if errs := gen.Validate(cat); len(errs) > 0 {
		t.Fatalf("Validate reported problems on clean fixture: %v", errs)
	}
}

func TestValidateRejectsGraphAndSchemaErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(t *testing.T, src string) string
		want   string
	}{
		{
			name: "empty catalog",
			mutate: func(_ *testing.T, _ string) string {
				return "schema_version = 1\n"
			},
			want: "defines no units",
		},
		{
			name: "duplicate unit id",
			mutate: func(_ *testing.T, src string) string {
				return src + `
[[unit]]
id = "baseline"
domain = "system"
stage = { linux = "baseline", macos = "baseline" }
privilege = { linux = "root", macos = "root" }
depends = { linux = [], macos = [] }
epic = "EPC-005"
milestone = "alpha"
`
			},
			want: "duplicate unit id: baseline",
		},
		{
			name: "stage unset but privilege set",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`privilege = { linux = "-", macos = "package-owner" }`,
					`privilege = { linux = "root", macos = "package-owner" }`)
			},
			want: `linux: unit packages-user has stage "-" but privilege "root"`,
		},
		{
			name: "unsupported privilege value",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`privilege = { linux = "root", macos = "root" }`,
					`privilege = { linux = "superadmin", macos = "root" }`)
			},
			want: `unsupported privilege "superadmin"`,
		},
		{
			name: "unsupported OS declares depends",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`depends = { linux = ["packages"], macos = [] }`,
					`depends = { linux = ["packages"], macos = ["packages"] }`)
			},
			want: "macos: unit agentctl does not run on macos but declares depends",
		},
		{
			name: "dangling depends on linux (unknown unit)",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`depends = { linux = ["baseline"], macos = ["packages-user"] }`,
					`depends = { linux = ["ghost"], macos = ["packages-user"] }`)
			},
			want: "linux: packages depends on ghost, which does not run on linux",
		},
		{
			name: "dangling depends on linux (macos-only unit)",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`depends = { linux = ["baseline"], macos = ["packages-user"] }`,
					`depends = { linux = ["packages-user"], macos = ["packages-user"] }`)
			},
			want: "linux: packages depends on packages-user, which does not run on linux",
		},
		{
			name: "dangling depends on macos (linux-only unit)",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`depends = { linux = ["packages"], macos = ["packages"] }`,
					`depends = { linux = ["packages"], macos = ["agentctl"] }`)
			},
			want: "macos: mesh-vpn depends on agentctl, which does not run on macos",
		},
		{
			name: "dangling depends_if on linux",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`depends_if = { remote-access = { linux = ["mesh-vpn"], macos = ["mesh-vpn"] } }`,
					`depends_if = { remote-access = { linux = ["packages-user"], macos = ["mesh-vpn"] } }`)
			},
			want: "linux: firewall depends_if[remote-access] on packages-user, which does not run on linux",
		},
		{
			name: "dangling depends_if on macos",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`depends_if = { remote-access = { linux = ["mesh-vpn"], macos = ["mesh-vpn"] } }`,
					`depends_if = { remote-access = { linux = ["mesh-vpn"], macos = ["agentctl"] } }`)
			},
			want: "macos: firewall depends_if[remote-access] on agentctl, which does not run on macos",
		},
		{
			name: "dangling waits_for unknown barrier",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`waits_for = ["s5-disarm"]`,
					`waits_for = ["no-such-barrier"]`)
			},
			want: `unit vpn waits_for unknown barrier "no-such-barrier"`,
		},
		{
			name: "dangling waits_for barrier producer absent on macos",
			mutate: func(t *testing.T, src string) string {
				// Make ssh linux-only while vpn still runs on macos and waits for s5-disarm.
				return replaceOnce(t, src,
					`id = "ssh"
domain = "remote"
stage = { linux = "remote", macos = "remote" }
privilege = { linux = "root", macos = "root" }
depends = { linux = ["firewall"], macos = ["firewall"] }
depends_if = { remote-access = { linux = ["mesh-vpn"], macos = ["mesh-vpn"] } }`,
					`id = "ssh"
domain = "remote"
stage = { linux = "remote", macos = "-" }
privilege = { linux = "root", macos = "-" }
depends = { linux = ["firewall"], macos = [] }
depends_if = { remote-access = { linux = ["mesh-vpn"], macos = [] } }`)
			},
			want: "macos: vpn waits_for s5-disarm, whose producer ssh does not run on macos",
		},
		{
			name: "cycle via depends on linux",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`depends = { linux = [], macos = [] }`,
					`depends = { linux = ["packages"], macos = [] }`)
			},
			want: "linux: dependency cycle:",
		},
		{
			name: "cycle via depends on macos",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`depends = { linux = [], macos = [] }`,
					`depends = { linux = [], macos = ["packages"] }`)
			},
			want: "macos: dependency cycle:",
		},
		{
			name: "self-cycle on linux",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`depends = { linux = [], macos = [] }`,
					`depends = { linux = ["baseline"], macos = [] }`)
			},
			want: "linux: dependency cycle: baseline -> baseline",
		},
		{
			name: "cycle via depends_if on linux",
			mutate: func(t *testing.T, src string) string {
				// firewall depends_if mesh-vpn; make mesh-vpn depend on firewall on linux.
				return replaceOnce(t, src,
					`id = "mesh-vpn"
domain = "network"
stage = { linux = "remote", macos = "remote" }
privilege = { linux = "root", macos = "root" }
depends = { linux = ["packages"], macos = ["packages"] }`,
					`id = "mesh-vpn"
domain = "network"
stage = { linux = "remote", macos = "remote" }
privilege = { linux = "root", macos = "root" }
depends = { linux = ["firewall"], macos = ["packages"] }`)
			},
			want: "linux: dependency cycle:",
		},
		{
			name: "cycle via depends_if on macos",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`id = "mesh-vpn"
domain = "network"
stage = { linux = "remote", macos = "remote" }
privilege = { linux = "root", macos = "root" }
depends = { linux = ["packages"], macos = ["packages"] }`,
					`id = "mesh-vpn"
domain = "network"
stage = { linux = "remote", macos = "remote" }
privilege = { linux = "root", macos = "root" }
depends = { linux = ["packages"], macos = ["firewall"] }`)
			},
			want: "macos: dependency cycle:",
		},
		{
			name: "cycle via waits_for (mesh-vpn waits for s5-disarm)",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`id = "mesh-vpn"
domain = "network"
stage = { linux = "remote", macos = "remote" }
privilege = { linux = "root", macos = "root" }
depends = { linux = ["packages"], macos = ["packages"] }`,
					`id = "mesh-vpn"
domain = "network"
stage = { linux = "remote", macos = "remote" }
privilege = { linux = "root", macos = "root" }
depends = { linux = ["packages"], macos = ["packages"] }
waits_for = ["s5-disarm"]`)
			},
			want: "dependency cycle:",
		},
		{
			name: "self-cycle via waits_for (firewall waits for s5-disarm)",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`depends_if = { remote-access = { linux = ["mesh-vpn"], macos = ["mesh-vpn"] } }`,
					`depends_if = { remote-access = { linux = ["mesh-vpn"], macos = ["mesh-vpn"] } }
waits_for = ["s5-disarm"]`)
			},
			want: "dependency cycle: firewall -> firewall",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := tc.mutate(t, minimalValidCatalog)
			cat, err := gen.ParseCatalog([]byte(src))
			if err != nil {
				t.Fatalf("ParseCatalog: %v", err)
			}
			errs := gen.Validate(cat)
			for _, e := range errs {
				if strings.Contains(e.Error(), tc.want) {
					return
				}
			}
			t.Fatalf("want an error containing %q, got %v", tc.want, errs)
		})
	}
}

func TestValidatePresets(t *testing.T) {
	t.Parallel()

	cat, err := gen.ParseCatalog([]byte(minimalValidCatalog))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}

	good := `
schema_version = 1

[tier.minimal]
units = ["baseline", "packages"]

[purpose.agent-host]
units = ["firewall", "ssh"]
`
	if errs := gen.ValidatePresets(cat, []byte(good)); len(errs) > 0 {
		t.Fatalf("clean presets failed: %v", errs)
	}

	bad := `
schema_version = 1

[tier.minimal]
units = ["baseline", "nonexistent"]
`
	errs := gen.ValidatePresets(cat, []byte(bad))
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "tier.minimal names unknown unit nonexistent") {
		t.Fatalf("want unknown unit error, got %v", errs)
	}
}

func TestWriteRepoGeneratesCleanCode(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, dir := range []string{
		"registry",
		filepath.Join("internal", "contract"),
		filepath.Join("internal", "units", "catalog"),
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "registry", "units.toml"), []byte(minimalValidCatalog), 0o600); err != nil {
		t.Fatalf("write units.toml: %v", err)
	}

	if err := gen.WriteRepo(root); err != nil {
		t.Fatalf("WriteRepo: %v", err)
	}

	rootFS := os.DirFS(root)
	paths := []string{
		"internal/contract/enums_gen.go",
		"internal/units/catalog/catalog_gen.go",
	}
	for _, p := range paths {
		data, err := fs.ReadFile(rootFS, p)
		if err != nil {
			t.Fatalf("read generated %s: %v", p, err)
		}
		firstLine, _, _ := strings.Cut(string(data), "\n")
		if !strings.HasPrefix(firstLine, "// Code generated ") || !strings.HasSuffix(firstLine, " DO NOT EDIT.") {
			t.Fatalf("%s missing generated header, got %q", p, firstLine)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, p, data, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok == token.VAR {
					t.Errorf("%s declares package-level var", p)
				}
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.Name == "init" {
					t.Errorf("%s declares init()", p)
				}
			}
		}
	}
}
