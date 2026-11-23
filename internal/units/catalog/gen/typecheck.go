package gen

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
)

const (
	contractPkgPath = "github.com/sathyassn/agent-workstation-kit/internal/contract"
	catalogPkgPath  = "github.com/sathyassn/agent-workstation-kit/internal/units/catalog"
)

type packageMapImporter struct {
	pkgs     map[string]*types.Package
	fallback types.Importer
}

func (m packageMapImporter) Import(path string) (*types.Package, error) {
	if pkg, ok := m.pkgs[path]; ok {
		return pkg, nil
	}
	pkg, err := m.fallback.Import(path)
	if err != nil {
		return nil, fmt.Errorf("import %q: %w", path, err)
	}
	return pkg, nil
}

// TypeCheckSources parses and type-checks the generated contract and catalog
// Go files together with any handwritten package files before writing to disk.
func TypeCheckSources(enumsSrc, catalogGenSrc []byte, extraContract, extraCatalog map[string][]byte) error {
	fset := token.NewFileSet()
	baseImporter := importer.Default()

	contractFiles, err := parsePackageSources(fset, "enums_gen.go", enumsSrc, extraContract)
	if err != nil {
		return fmt.Errorf("parse contract package: %w", err)
	}
	contractCfg := &types.Config{Importer: baseImporter}
	contractPkg, err := contractCfg.Check(contractPkgPath, fset, contractFiles, nil)
	if err != nil {
		return fmt.Errorf("type-check contract package: %w", err)
	}

	catalogFiles, err := parsePackageSources(fset, "catalog_gen.go", catalogGenSrc, extraCatalog)
	if err != nil {
		return fmt.Errorf("parse catalog package: %w", err)
	}
	catalogCfg := &types.Config{
		Importer: packageMapImporter{
			pkgs:     map[string]*types.Package{contractPkgPath: contractPkg},
			fallback: baseImporter,
		},
	}
	if _, err := catalogCfg.Check(catalogPkgPath, fset, catalogFiles, nil); err != nil {
		return fmt.Errorf("type-check catalog package: %w", err)
	}
	return nil
}

func parsePackageSources(fset *token.FileSet, genName string, genSrc []byte, extra map[string][]byte) ([]*ast.File, error) {
	files := make([]*ast.File, 0, 1+len(extra))
	genFile, err := parser.ParseFile(fset, genName, genSrc, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", genName, err)
	}
	files = append(files, genFile)

	for _, name := range sortedMapKeys(extra) {
		f, err := parser.ParseFile(fset, name, extra[name], 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		files = append(files, f)
	}
	return files, nil
}

func readHandwrittenGoFiles(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", dir, err)
	}
	out := make(map[string][]byte)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path) //nolint:gosec // path is inside caller-supplied repository root
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		out[name] = data
	}
	return out, nil
}
