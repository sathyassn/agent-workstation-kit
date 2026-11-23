package catalog_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wskit "github.com/sathyassn/agent-workstation-kit"
	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/units/catalog"
	"github.com/sathyassn/agent-workstation-kit/internal/units/catalog/gen"
)

func TestGeneratedFilesCurrent(t *testing.T) {
	t.Parallel()

	unitsData, err := fs.ReadFile(wskit.RegistryFS(), "registry/units.toml")
	if err != nil {
		t.Fatalf("read embedded registry/units.toml: %v", err)
	}
	cat, err := gen.ParseCatalog(unitsData)
	if err != nil {
		t.Fatalf("parse registry/units.toml: %v", err)
	}
	if errs := gen.Validate(cat); len(errs) > 0 {
		t.Fatalf("registry/units.toml validation failed: %v", errs)
	}

	presetsData, err := fs.ReadFile(wskit.RegistryFS(), "registry/presets.toml")
	if err != nil {
		t.Fatalf("read embedded registry/presets.toml: %v", err)
	}
	if errs := gen.ValidatePresets(cat, presetsData); len(errs) > 0 {
		t.Fatalf("registry/presets.toml validation failed: %v", errs)
	}

	want, err := gen.Generate(cat)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	checks := []struct {
		name string
		path string
		want []byte
	}{
		{
			name: "contract enums",
			path: filepath.Join("..", "..", "contract", "enums_gen.go"),
			want: want.ContractEnums,
		},
		{
			name: "catalog table",
			path: "catalog_gen.go",
			want: want.CatalogTable,
		},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatalf("read %s: %v", tc.path, err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("%s is stale; run `go generate ./...`", tc.path)
			}
		})
	}

	repoFS := os.DirFS(filepath.Join("..", "..", ".."))
	for _, rel := range gen.DocTargets() {
		t.Run("docs table "+rel, func(t *testing.T) {
			t.Parallel()
			got, err := fs.ReadFile(repoFS, rel)
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			expected, err := gen.ReplaceDocsTable(got, want.DocsTable)
			if err != nil {
				t.Fatalf("ReplaceDocsTable(%s): %v", rel, err)
			}
			if !bytes.Equal(got, expected) {
				t.Fatalf("%s catalog table is stale; run `go generate ./...`", rel)
			}
		})
	}
}

func TestStaleOutputDetectedWhenCatalogChanges(t *testing.T) {
	t.Parallel()

	unitsData, err := fs.ReadFile(wskit.RegistryFS(), "registry/units.toml")
	if err != nil {
		t.Fatalf("read embedded registry/units.toml: %v", err)
	}
	mutated := string(unitsData) + `
[[unit]]
id = "extra-unit"
domain = "ops"
stage = { linux = "audit", macos = "audit" }
privilege = { linux = "root", macos = "root" }
depends = { linux = [], macos = [] }
epic = "EPC-999"
milestone = "alpha"
`
	cat, err := gen.ParseCatalog([]byte(mutated))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	fresh, err := gen.Generate(cat)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	committedEnums, err := os.ReadFile(filepath.Join("..", "..", "contract", "enums_gen.go"))
	if err != nil {
		t.Fatalf("read enums_gen.go: %v", err)
	}
	if bytes.Equal(committedEnums, fresh.ContractEnums) {
		t.Fatal("expected enums_gen.go to differ when a unit is added to units.toml")
	}
	committedCatalog, err := os.ReadFile("catalog_gen.go")
	if err != nil {
		t.Fatalf("read catalog_gen.go: %v", err)
	}
	if bytes.Equal(committedCatalog, fresh.CatalogTable) {
		t.Fatal("expected catalog_gen.go to differ when a unit is added to units.toml")
	}
}

func TestGeneratedFilesHaveNoMutableStateOrInit(t *testing.T) {
	t.Parallel()

	files := []struct {
		dir  string
		name string
	}{
		{dir: filepath.Join("..", "..", "contract"), name: "enums_gen.go"},
		{dir: ".", name: "catalog_gen.go"},
	}

	for _, f := range files {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			data, err := fs.ReadFile(os.DirFS(f.dir), f.name)
			if err != nil {
				t.Fatalf("read %s: %v", f.name, err)
			}
			firstLine, _, _ := strings.Cut(string(data), "\n")
			if !strings.HasPrefix(firstLine, "// Code generated ") || !strings.HasSuffix(firstLine, " DO NOT EDIT.") {
				t.Fatalf("%s missing standard generated header, got %q", f.name, firstLine)
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, f.name, data, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", f.name, err)
			}
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.GenDecl:
					if d.Tok == token.VAR {
						t.Errorf("%s must not declare package-level var", f.name)
					}
				case *ast.FuncDecl:
					if d.Recv == nil && d.Name.Name == "init" {
						t.Errorf("%s must not declare init()", f.name)
					}
				}
			}
		})
	}
}

func TestCatalogMetadata(t *testing.T) {
	t.Parallel()

	entries := catalog.Entries()
	allIDs := contract.AllUnitIDs()
	if len(entries) != len(allIDs) {
		t.Fatalf("len(Entries()) = %d, want %d", len(entries), len(allIDs))
	}

	for i, e := range entries {
		if e.ID != allIDs[i] {
			t.Errorf("entry %d ID = %q, want %q", i, e.ID, allIDs[i])
		}
		if !e.ID.Valid() {
			t.Errorf("entry %d has invalid ID %q", i, e.ID)
		}
		if !e.Domain.Valid() {
			t.Errorf("entry %s has invalid Domain %q", e.ID, e.Domain)
		}
		for _, pm := range []catalog.PlatformMeta{e.Linux, e.MacOS} {
			if !pm.Supported {
				continue
			}
			if !pm.Stage.Valid() {
				t.Errorf("entry %s has invalid Stage %q", e.ID, pm.Stage)
			}
			if !pm.Privilege.Valid() {
				t.Errorf("entry %s has invalid Privilege %q", e.ID, pm.Privilege)
			}
			if !pm.Scope.Valid() {
				t.Errorf("entry %s has invalid Scope %q", e.ID, pm.Scope)
			}
			if pm.Scope != pm.Privilege.Scope() {
				t.Errorf("entry %s scope %q != privilege scope %q", e.ID, pm.Scope, pm.Privilege.Scope())
			}
		}
		got, ok := catalog.Lookup(e.ID)
		if !ok || got.ID != e.ID {
			t.Errorf("Lookup(%q) = (%v, %v), want found", e.ID, got.ID, ok)
		}
	}

	if _, ok := catalog.Lookup(contract.UnitID("nonexistent")); ok {
		t.Error("Lookup(nonexistent) unexpectedly succeeded")
	}

	// Verify Entries returns a fresh copy (no shared mutable state).
	entries[0].ID = contract.UnitID("mutated")
	if catalog.Entries()[0].ID != contract.UnitBaseline {
		t.Fatal("mutating slice returned by Entries() affected subsequent calls")
	}

	// Spot-check conditional dependencies and barriers.
	fw, ok := catalog.Lookup(contract.UnitFirewall)
	if !ok || len(fw.Linux.DependsIf) != 1 || fw.Linux.DependsIf[0].Condition != catalog.ConditionRemoteAccess {
		t.Fatalf("unexpected firewall DependsIf: %+v", fw.Linux.DependsIf)
	}
	vpn, ok := catalog.Lookup(contract.UnitVPN)
	if !ok || len(vpn.WaitsFor) != 1 || vpn.WaitsFor[0] != catalog.BarrierS5Disarm {
		t.Fatalf("unexpected vpn WaitsFor: %+v", vpn.WaitsFor)
	}
	pkgUser, ok := catalog.Lookup(contract.UnitPackagesUser)
	if !ok || pkgUser.Linux.Supported || !pkgUser.MacOS.Supported {
		t.Fatalf("unexpected packages-user platform support: linux=%v macos=%v", pkgUser.Linux.Supported, pkgUser.MacOS.Supported)
	}
}
