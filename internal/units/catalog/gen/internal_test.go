package gen

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReservedGoNamesCoversEmittedDeclarations(t *testing.T) {
	t.Parallel()

	const fixture = `
schema_version = 1

[[unit]]
id = "baseline"
domain = "system"
stage = { linux = "baseline", macos = "baseline" }
privilege = { linux = "root", macos = "root" }
depends = { linux = [], macos = [] }
epic = "EPC-005"
milestone = "alpha"
`
	cat, err := ParseCatalog([]byte(fixture))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	out, err := Generate(cat)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	reserved := reservedGoNames()
	sources := map[string][]byte{
		"enums_gen.go":   out.ContractEnums,
		"catalog_gen.go": out.CatalogTable,
	}
	for name, src := range sources {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok != token.TYPE && d.Tok != token.VAR {
					continue
				}
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if !reserved[s.Name.Name] {
							t.Errorf("%s emits type %s not present in reservedGoNames()", name, s.Name.Name)
						}
					case *ast.ValueSpec:
						for _, ident := range s.Names {
							if !reserved[ident.Name] {
								t.Errorf("%s emits var %s not present in reservedGoNames()", name, ident.Name)
							}
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv != nil {
					continue
				}
				if !reserved[d.Name.Name] {
					t.Errorf("%s emits func %s not present in reservedGoNames()", name, d.Name.Name)
				}
			}
		}
	}
}

func TestWriteRepoAtomicInjectedSecondWriteFailure(t *testing.T) {
	t.Parallel()

	const initialCatalog = `
schema_version = 1

[[unit]]
id = "baseline"
domain = "system"
stage = { linux = "baseline", macos = "baseline" }
privilege = { linux = "root", macos = "root" }
depends = { linux = [], macos = [] }
epic = "EPC-005"
milestone = "alpha"
`
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
	if err := os.WriteFile(filepath.Join(root, "registry", "units.toml"), []byte(initialCatalog), 0o600); err != nil {
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
	for _, rel := range DocTargets() {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.WriteFile(abs, []byte(docTemplate), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	if err := WriteRepo(root); err != nil {
		t.Fatalf("initial WriteRepo: %v", err)
	}

	targets := []string{
		"internal/contract/enums_gen.go",
		"internal/units/catalog/catalog_gen.go",
	}
	targets = append(targets, DocTargets()...)
	before := make(map[string][]byte, len(targets))
	rootFS := os.DirFS(root)
	for _, rel := range targets {
		data, err := fs.ReadFile(rootFS, rel)
		if err != nil {
			t.Fatalf("read initial %s: %v", rel, err)
		}
		before[rel] = data
	}

	updatedCatalog := initialCatalog + `
[[unit]]
id = "extra-unit"
domain = "ops"
stage = { linux = "audit", macos = "audit" }
privilege = { linux = "root", macos = "root" }
depends = { linux = [], macos = [] }
epic = "EPC-999"
milestone = "alpha"
`
	if err := os.WriteFile(filepath.Join(root, "registry", "units.toml"), []byte(updatedCatalog), 0o600); err != nil {
		t.Fatalf("write updated units.toml: %v", err)
	}

	injected := errors.New("injected second write failure")
	calls := 0
	ops := defaultFileOps()
	ops.writeTemp = func(targetPath string, data []byte) (string, error) {
		calls++
		if calls == 2 {
			return "", injected
		}
		return writeTempFile(targetPath, data)
	}

	err = writeRepoWithOps(root, ops)
	if !errors.Is(err, injected) {
		t.Fatalf("writeRepoWithOps() error = %v, want injected error", err)
	}

	for _, rel := range targets {
		after, err := fs.ReadFile(rootFS, rel)
		if err != nil {
			t.Fatalf("read %s after failure: %v", rel, err)
		}
		if !bytes.Equal(before[rel], after) {
			t.Errorf("target %s was modified despite injected failure on second write", rel)
		}
	}

	for _, rel := range targets {
		dir := filepath.Join(root, filepath.Dir(filepath.FromSlash(rel)))
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("readdir %s: %v", dir, err)
		}
		for _, entry := range entries {
			if strings.Contains(entry.Name(), ".tmp-") {
				t.Errorf("leftover temp file %s in %s", entry.Name(), dir)
			}
		}
	}
}
