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

	unknownTopKey := `
schema_version = 1
extra_top_key = true

[tier.minimal]
units = ["baseline"]
`
	if errs := gen.ValidatePresets(cat, []byte(unknownTopKey)); len(errs) == 0 || !strings.Contains(errs[0].Error(), "unknown keys") {
		t.Fatalf("want unknown keys error for top-level key, got %v", errs)
	}

	unknownNestedKey := `
schema_version = 1

[tier.minimal]
units = ["baseline"]
bogus = 42
`
	if errs := gen.ValidatePresets(cat, []byte(unknownNestedKey)); len(errs) == 0 || !strings.Contains(errs[0].Error(), "unknown keys") {
		t.Fatalf("want unknown keys error for nested key, got %v", errs)
	}
}

func TestValidateRejectsInvalidIdentifiersAndCollisions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(t *testing.T, src string) string
		want   string
	}{
		{
			name: "invalid unit id (uppercase and underscore)",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src, `id = "baseline"`, `id = "Bad_ID"`)
			},
			want: `unit id "Bad_ID" is not a valid identifier`,
		},
		{
			name: "invalid stage token",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`stage = { linux = "baseline", macos = "baseline" }`,
					`stage = { linux = "base_line", macos = "baseline" }`)
			},
			want: `invalid stage "base_line"`,
		},
		{
			name: "invalid domain token",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src, `domain = "system"`, `domain = "System!"`)
			},
			want: `invalid domain "System!"`,
		},
		{
			name: "invalid privilege token",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`privilege = { linux = "root", macos = "root" }`,
					`privilege = { linux = "Root_User", macos = "root" }`)
			},
			want: `invalid privilege "Root_User"`,
		},
		{
			name: "invalid condition token",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`depends_if = { remote-access = { linux = ["mesh-vpn"], macos = ["mesh-vpn"] } }`,
					`depends_if = { remote_access = { linux = ["mesh-vpn"], macos = ["mesh-vpn"] } }`)
			},
			want: `invalid depends_if condition "remote_access"`,
		},
		{
			name: "invalid barrier token",
			mutate: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					`waits_for = ["s5-disarm"]`,
					`waits_for = ["S5_Disarm"]`)
			},
			want: `invalid waits_for barrier "S5_Disarm"`,
		},
		{
			name: "Go identifier collision ssh vs s-s-h",
			mutate: func(_ *testing.T, src string) string {
				return src + `
[[unit]]
id = "s-s-h"
domain = "remote"
stage = { linux = "remote", macos = "remote" }
privilege = { linux = "root", macos = "root" }
depends = { linux = [], macos = [] }
epic = "EPC-015"
milestone = "alpha"
`
			},
			want: `collide on Go identifier UnitSSH`,
		},
		{
			name: "Go identifier collision foo-bar vs foo_bar",
			mutate: func(_ *testing.T, src string) string {
				return src + `
[[unit]]
id = "foo-bar"
domain = "ops"
stage = { linux = "audit", macos = "audit" }
privilege = { linux = "root", macos = "root" }
depends = { linux = [], macos = [] }
epic = "EPC-999"
milestone = "alpha"

[[unit]]
id = "foo_bar"
domain = "ops"
stage = { linux = "audit", macos = "audit" }
privilege = { linux = "root", macos = "root" }
depends = { linux = [], macos = [] }
epic = "EPC-999"
milestone = "alpha"
`
			},
			want: `collide on Go identifier UnitFooBar`,
		},
		{
			name: "reserved type name collision unit id",
			mutate: func(_ *testing.T, src string) string {
				return src + `
[[unit]]
id = "id"
domain = "ops"
stage = { linux = "audit", macos = "audit" }
privilege = { linux = "root", macos = "root" }
depends = { linux = [], macos = [] }
epic = "EPC-999"
milestone = "alpha"
`
			},
			want: `collides with reserved Go identifier UnitID`,
		},
		{
			name: "reserved helper name collision lookup",
			mutate: func(_ *testing.T, src string) string {
				return src + `
[[unit]]
id = "lookup"
domain = "ops"
stage = { linux = "audit", macos = "audit" }
privilege = { linux = "root", macos = "root" }
depends = { linux = [], macos = [] }
epic = "EPC-999"
milestone = "alpha"
`
			},
			want: `collides with reserved Go identifier Lookup`,
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

func setupTempRepo(t *testing.T, unitsTOML string) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{
		"registry",
		filepath.Join("internal", "contract"),
		filepath.Join("internal", "units", "catalog"),
		filepath.Join("docs", "plan"),
		filepath.Join("docs", "design"),
		filepath.Join("project-management", "specs"),
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "registry", "units.toml"), []byte(unitsTOML), 0o600); err != nil {
		t.Fatalf("write units.toml: %v", err)
	}
	catalogSrc, err := fs.ReadFile(os.DirFS(".."), "catalog.go")
	if err != nil {
		t.Fatalf("read catalog.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "units", "catalog", "catalog.go"), catalogSrc, 0o600); err != nil { //nolint:gosec // test temp dir
		t.Fatalf("write catalog.go: %v", err)
	}
	const docTemplate = "# Doc\n\n<!-- catalog:units:begin -->\nold\n<!-- catalog:units:end -->\n"
	for _, rel := range gen.DocTargets() {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.WriteFile(abs, []byte(docTemplate), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

func TestWriteRepoGeneratesCleanCode(t *testing.T) {
	t.Parallel()

	root := setupTempRepo(t, minimalValidCatalog)
	if err := gen.WriteRepo(root); err != nil {
		t.Fatalf("WriteRepo: %v", err)
	}
	if err := gen.CheckRepo(root); err != nil {
		t.Fatalf("CheckRepo after WriteRepo: %v", err)
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

func TestSingleEditStalesGoAndDocsAndRegeneratesBoth(t *testing.T) {
	t.Parallel()

	root := setupTempRepo(t, minimalValidCatalog)
	if err := gen.WriteRepo(root); err != nil {
		t.Fatalf("initial WriteRepo: %v", err)
	}
	if err := gen.CheckRepo(root); err != nil {
		t.Fatalf("initial CheckRepo: %v", err)
	}

	edited := replaceOnce(t, minimalValidCatalog, `epic = "EPC-005"`, `epic = "EPC-777"`)
	if err := os.WriteFile(filepath.Join(root, "registry", "units.toml"), []byte(edited), 0o600); err != nil {
		t.Fatalf("write edited units.toml: %v", err)
	}

	err := gen.CheckRepo(root)
	if err == nil {
		t.Fatal("CheckRepo unexpectedly passed after editing units.toml")
	}
	msg := err.Error()
	for _, wantStale := range []string{
		"internal/units/catalog/catalog_gen.go",
		"docs/plan/roadmap.md",
		"docs/design/setup-architecture.md",
		"project-management/specs/SPC-001.md",
	} {
		if !strings.Contains(msg, wantStale) {
			t.Errorf("CheckRepo error missing stale file %q:\n%s", wantStale, msg)
		}
	}

	if err := gen.WriteRepo(root); err != nil {
		t.Fatalf("regeneration WriteRepo: %v", err)
	}
	if err := gen.CheckRepo(root); err != nil {
		t.Fatalf("CheckRepo after regeneration: %v", err)
	}
	rootFS := os.DirFS(root)
	for _, rel := range gen.DocTargets() {
		data, err := fs.ReadFile(rootFS, rel)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !strings.Contains(string(data), "EPC-777") {
			t.Errorf("%s was not refreshed with EPC-777", rel)
		}
	}
}

func TestWriteRepoWritesNothingOnCollisionOrTypeCheckFailure(t *testing.T) {
	t.Parallel()

	t.Run("collision writes nothing", func(t *testing.T) {
		t.Parallel()
		colliding := minimalValidCatalog + `
[[unit]]
id = "s-s-h"
domain = "remote"
stage = { linux = "remote", macos = "remote" }
privilege = { linux = "root", macos = "root" }
depends = { linux = [], macos = [] }
epic = "EPC-015"
milestone = "alpha"
`
		root := setupTempRepo(t, colliding)
		err := gen.WriteRepo(root)
		if err == nil || !strings.Contains(err.Error(), "collide on Go identifier UnitSSH") {
			t.Fatalf("WriteRepo() error = %v, want collision error", err)
		}
		for _, p := range []string{
			filepath.Join(root, "internal", "contract", "enums_gen.go"),
			filepath.Join(root, "internal", "units", "catalog", "catalog_gen.go"),
		} {
			if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
				t.Errorf("expected %s not to be written on collision", p)
			}
		}
	})

	t.Run("type-check failure writes nothing", func(t *testing.T) {
		t.Parallel()
		root := setupTempRepo(t, minimalValidCatalog)
		// Break catalog.go so the generated catalog_gen.go cannot type-check.
		if err := os.WriteFile(filepath.Join(root, "internal", "units", "catalog", "catalog.go"), []byte("package catalog\n"), 0o600); err != nil {
			t.Fatalf("overwrite catalog.go: %v", err)
		}
		err := gen.WriteRepo(root)
		if err == nil || !strings.Contains(err.Error(), "type-check catalog package") {
			t.Fatalf("WriteRepo() error = %v, want type-check error", err)
		}
		for _, p := range []string{
			filepath.Join(root, "internal", "contract", "enums_gen.go"),
			filepath.Join(root, "internal", "units", "catalog", "catalog_gen.go"),
		} {
			if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
				t.Errorf("expected %s not to be written when type-checking fails", p)
			}
		}
	})

	t.Run("TypeCheckSources rejects broken contract source", func(t *testing.T) {
		t.Parallel()
		err := gen.TypeCheckSources([]byte("package contract\nvar _ int = \"not-an-int\"\n"), []byte("package catalog\n"), nil, nil)
		if err == nil || !strings.Contains(err.Error(), "type-check contract package") {
			t.Fatalf("TypeCheckSources() error = %v, want contract type-check error", err)
		}
	})
}

func TestDocTargetErrorsFailCheckAndWrite(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(t *testing.T, targetPath string)
		want   string
	}{
		{
			name: "missing target doc",
			mutate: func(t *testing.T, targetPath string) {
				if err := os.Remove(targetPath); err != nil {
					t.Fatalf("remove target doc: %v", err)
				}
			},
			want: "docs/plan/roadmap.md",
		},
		{
			name: "missing begin marker",
			mutate: func(t *testing.T, targetPath string) {
				if err := os.WriteFile(targetPath, []byte("# Doc\n<!-- catalog:units:end -->\n"), 0o600); err != nil {
					t.Fatalf("write doc: %v", err)
				}
			},
			want: "missing catalog:units:begin marker",
		},
		{
			name: "missing end marker",
			mutate: func(t *testing.T, targetPath string) {
				if err := os.WriteFile(targetPath, []byte("# Doc\n<!-- catalog:units:begin -->\n"), 0o600); err != nil {
					t.Fatalf("write doc: %v", err)
				}
			},
			want: "missing catalog:units:end marker",
		},
		{
			name: "duplicate begin marker",
			mutate: func(t *testing.T, targetPath string) {
				doc := "# Doc\n<!-- catalog:units:begin -->\n<!-- catalog:units:begin -->\n<!-- catalog:units:end -->\n"
				if err := os.WriteFile(targetPath, []byte(doc), 0o600); err != nil {
					t.Fatalf("write doc: %v", err)
				}
			},
			want: "duplicate catalog:units:begin marker",
		},
		{
			name: "duplicate end marker",
			mutate: func(t *testing.T, targetPath string) {
				doc := "# Doc\n<!-- catalog:units:begin -->\n<!-- catalog:units:end -->\n<!-- catalog:units:end -->\n"
				if err := os.WriteFile(targetPath, []byte(doc), 0o600); err != nil {
					t.Fatalf("write doc: %v", err)
				}
			},
			want: "duplicate catalog:units:end marker",
		},
		{
			name: "end marker before begin marker",
			mutate: func(t *testing.T, targetPath string) {
				doc := "# Doc\n<!-- catalog:units:end -->\n<!-- catalog:units:begin -->\n"
				if err := os.WriteFile(targetPath, []byte(doc), 0o600); err != nil {
					t.Fatalf("write doc: %v", err)
				}
			},
			want: "appears before",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := setupTempRepo(t, minimalValidCatalog)
			if err := gen.WriteRepo(root); err != nil {
				t.Fatalf("initial WriteRepo: %v", err)
			}
			target := filepath.Join(root, filepath.FromSlash(gen.DocTargets()[0]))
			tc.mutate(t, target)

			if err := gen.CheckRepo(root); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("CheckRepo() error = %v, want substring %q", err, tc.want)
			}
			if err := gen.WriteRepo(root); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("WriteRepo() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestWriteRepoSecondWriteFailureLeavesTargetsUnchanged(t *testing.T) {
	t.Parallel()

	root := setupTempRepo(t, minimalValidCatalog)
	if err := gen.WriteRepo(root); err != nil {
		t.Fatalf("initial WriteRepo: %v", err)
	}

	enumsPath := filepath.Join(root, "internal", "contract", "enums_gen.go")
	origEnums, err := os.ReadFile(enumsPath) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatalf("read initial enums_gen.go: %v", err)
	}
	catalogPath := filepath.Join(root, "internal", "units", "catalog", "catalog_gen.go")
	if err := os.Remove(catalogPath); err != nil {
		t.Fatalf("remove catalog_gen.go: %v", err)
	}
	catalogDir := filepath.Dir(catalogPath)
	if err := os.Chmod(catalogDir, 0o500); err != nil { //nolint:gosec // test temp dir read-only to trigger write failure
		t.Fatalf("chmod catalog dir read-only: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(catalogDir, 0o750) //nolint:gosec // restore test temp dir permissions for cleanup
	})

	edited := minimalValidCatalog + `
[[unit]]
id = "extra-unit"
domain = "ops"
stage = { linux = "audit", macos = "audit" }
privilege = { linux = "root", macos = "root" }
depends = { linux = [], macos = [] }
epic = "EPC-999"
milestone = "alpha"
`
	if err := os.WriteFile(filepath.Join(root, "registry", "units.toml"), []byte(edited), 0o600); err != nil {
		t.Fatalf("write edited units.toml: %v", err)
	}

	if err := gen.WriteRepo(root); err == nil {
		t.Fatal("WriteRepo unexpectedly succeeded when catalog dir is read-only")
	}

	gotEnums, err := os.ReadFile(enumsPath) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatalf("read enums_gen.go after failure: %v", err)
	}
	if string(gotEnums) != string(origEnums) {
		t.Fatal("enums_gen.go was modified despite failure on second write")
	}
}
