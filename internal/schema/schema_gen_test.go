package schema_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wskit "github.com/sathyassn/agent-workstation-kit"
	"github.com/sathyassn/agent-workstation-kit/internal/schema"
	"github.com/sathyassn/agent-workstation-kit/internal/units/catalog/gen"
)

// TestGeneratedSchemaCurrent covers AC-4: when schema/profile.toml changes
// without regenerating internal/schema/types_gen.go, go test ./... fails and
// names the stale generated file.
func TestGeneratedSchemaCurrent(t *testing.T) {
	t.Parallel()

	schemaData, err := fs.ReadFile(wskit.SchemaFS(), "schema/profile.toml")
	if err != nil {
		t.Fatalf("read embedded schema/profile.toml: %v", err)
	}
	ps, err := gen.ParseProfileSchema(schemaData)
	if err != nil {
		t.Fatalf("ParseProfileSchema: %v", err)
	}
	if errs := gen.ValidateProfileSchema(ps); len(errs) > 0 {
		t.Fatalf("ValidateProfileSchema failed: %v", errs)
	}
	want, err := gen.RenderSchemaTypes(ps)
	if err != nil {
		t.Fatalf("RenderSchemaTypes: %v", err)
	}
	got, err := os.ReadFile("types_gen.go")
	if err != nil {
		t.Fatalf("read internal/schema/types_gen.go: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("internal/schema/types_gen.go is stale; run `go generate ./...`")
	}
}

// TestStaleSchemaDetectedWhenSchemaChanges covers AC-4 by mutating
// schema/profile.toml in a temp copy of the repo, verifying gen.CheckRepo
// fails and names internal/schema/types_gen.go, then regenerating and proving
// gen.CheckRepo passes.
func TestStaleSchemaDetectedWhenSchemaChanges(t *testing.T) {
	t.Parallel()

	repoRoot := filepath.Join("..", "..")
	root := t.TempDir()
	for _, dir := range []string{
		"registry",
		"schema",
		filepath.Join("internal", "contract"),
		filepath.Join("internal", "units", "catalog"),
		filepath.Join("internal", "schema"),
		filepath.Join("docs", "plan"),
		filepath.Join("docs", "design"),
		filepath.Join("project-management", "specs"),
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	copyFile := func(rel string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel))) //nolint:gosec // repo fixture copy
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), data, 0o600); err != nil { //nolint:gosec // temp repo copy
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	copyFile("registry/units.toml")
	copyFile("registry/presets.toml")
	copyFile("schema/profile.toml")
	copyFile("internal/units/catalog/catalog.go")
	for _, rel := range gen.DocTargets() {
		copyFile(rel)
	}

	if err := gen.WriteRepo(root); err != nil {
		t.Fatalf("initial WriteRepo: %v", err)
	}
	if err := gen.CheckRepo(root); err != nil {
		t.Fatalf("initial CheckRepo: %v", err)
	}

	// Edit schema/profile.toml without regenerating.
	schemaPath := filepath.Join(root, "schema", "profile.toml")
	orig, err := os.ReadFile(schemaPath) //nolint:gosec // temp repo path
	if err != nil {
		t.Fatalf("read temp schema: %v", err)
	}
	mutated := string(orig) + `
[[field]]
key = "backup.verify_schedule"
type = "string"
required = false
stage = "resources"
what = "Optional backup verification schedule."
why = "Allows testing staleness detection."
risk = "None."
undo = "Remove field."
`
	if err := os.WriteFile(schemaPath, []byte(mutated), 0o600); err != nil { //nolint:gosec // temp repo path
		t.Fatalf("write mutated schema: %v", err)
	}

	err = gen.CheckRepo(root)
	if err == nil {
		t.Fatal("CheckRepo unexpectedly passed after mutating schema/profile.toml")
	}
	if !strings.Contains(err.Error(), "internal/schema/types_gen.go") {
		t.Fatalf("CheckRepo error = %q, want it to name internal/schema/types_gen.go", err.Error())
	}

	if err := gen.WriteRepo(root); err != nil {
		t.Fatalf("regeneration WriteRepo: %v", err)
	}
	if err := gen.CheckRepo(root); err != nil {
		t.Fatalf("CheckRepo after regeneration: %v", err)
	}
}

func TestGeneratedSchemaHasNoMutableStateOrInit(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("types_gen.go")
	if err != nil {
		t.Fatalf("read types_gen.go: %v", err)
	}
	firstLine, _, _ := strings.Cut(string(data), "\n")
	if !strings.HasPrefix(firstLine, "// Code generated ") || !strings.HasSuffix(firstLine, " DO NOT EDIT.") {
		t.Fatalf("types_gen.go missing standard generated header: %q", firstLine)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "types_gen.go", data, 0)
	if err != nil {
		t.Fatalf("parse types_gen.go: %v", err)
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok == token.VAR {
				t.Error("types_gen.go must not declare package-level var")
			}
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.Name == "init" {
				t.Error("types_gen.go must not declare init()")
			}
		}
	}
}

func TestGeneratedEnumsAndKeysDecodeValidation(t *testing.T) {
	t.Parallel()

	var pk schema.ProfileKind
	if err := pk.UnmarshalText([]byte("work")); err != nil || pk != schema.ProfileKindWork {
		t.Fatalf("UnmarshalText(work) = (%q, %v)", pk, err)
	}
	if err := pk.UnmarshalText([]byte("bogus")); !errors.Is(err, schema.ErrUnknownValue) {
		t.Fatalf("UnmarshalText(bogus) = %v, want ErrUnknownValue", err)
	}
	if err := json.Unmarshal([]byte("null"), &pk); !errors.Is(err, schema.ErrUnknownValue) {
		t.Fatalf("UnmarshalJSON(null) = %v, want ErrUnknownValue", err)
	}

	var k schema.Key
	if err := k.UnmarshalText([]byte("backup.retention_days")); err != nil || k != schema.KeyBackupRetentionDays {
		t.Fatalf("Key.UnmarshalText = (%q, %v)", k, err)
	}
	if err := k.UnmarshalText([]byte("no.such.key")); !errors.Is(err, schema.ErrUnknownValue) {
		t.Fatalf("Key.UnmarshalText(invalid) = %v, want ErrUnknownValue", err)
	}
	if err := json.Unmarshal([]byte("null"), &k); !errors.Is(err, schema.ErrUnknownValue) {
		t.Fatalf("Key.UnmarshalJSON(null) = %v, want ErrUnknownValue", err)
	}

	// Returned slices must be fresh copies (no shared mutable state).
	specs := schema.FieldSpecs()
	if len(specs) == 0 {
		t.Fatal("FieldSpecs() returned empty slice")
	}
	origKey := specs[0].Key
	specs[0].Key = schema.Key("mutated")
	if schema.FieldSpecs()[0].Key != origKey {
		t.Fatal("mutating FieldSpecs() affected subsequent calls")
	}
}
