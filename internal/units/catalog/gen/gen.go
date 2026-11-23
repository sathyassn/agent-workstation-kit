// Package gen parses, validates and generates Go code from registry/units.toml.
package gen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	osLinux = "linux"
	osMacOS = "macos"

	unsetMarker = "-"

	privRoot         = "root"
	privPackageOwner = "package-owner"
	privAgent        = "agent"
	privUser         = "user"

	scopeRoot = "root"
	scopeUser = "user"

	unitSSH = "ssh"
)

// Catalog is the decoded registry/units.toml document.
type Catalog struct {
	SchemaVersion int        `toml:"schema_version"`
	Units         []UnitSpec `toml:"unit"`
}

// OSString holds one string value per supported OS.
type OSString struct {
	Linux string `toml:"linux"`
	MacOS string `toml:"macos"`
}

// OSStringList holds a list of string values per supported OS.
type OSStringList struct {
	Linux []string `toml:"linux"`
	MacOS []string `toml:"macos"`
}

// UnitSpec is one [[unit]] entry from registry/units.toml.
type UnitSpec struct {
	ID        string                  `toml:"id"`
	Domain    string                  `toml:"domain"`
	Stage     OSString                `toml:"stage"`
	Privilege OSString                `toml:"privilege"`
	Depends   OSStringList            `toml:"depends"`
	DependsIf map[string]OSStringList `toml:"depends_if"`
	WaitsFor  []string                `toml:"waits_for"`
	Epic      string                  `toml:"epic"`
	Milestone string                  `toml:"milestone"`
}

type presetsDoc struct {
	SchemaVersion int                    `toml:"schema_version"`
	Tier          map[string]presetEntry `toml:"tier"`
	Purpose       map[string]presetEntry `toml:"purpose"`
}

type presetEntry struct {
	Units []string `toml:"units"`
}

// ParseCatalog decodes registry/units.toml and rejects unknown keys or
// unsupported schema versions.
func ParseCatalog(data []byte) (Catalog, error) {
	var cat Catalog
	md, err := toml.Decode(string(data), &cat)
	if err != nil {
		return Catalog{}, fmt.Errorf("decode units catalog: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return Catalog{}, fmt.Errorf("units catalog has unknown keys: %v", undecoded)
	}
	if cat.SchemaVersion != 1 {
		return Catalog{}, fmt.Errorf("units catalog schema_version = %d, want 1", cat.SchemaVersion)
	}
	return cat, nil
}

// knownBarriers maps each waits_for barrier to the unit IDs that produce it.
func knownBarriers() map[string][]string {
	return map[string][]string{
		"s5-disarm": {"firewall", unitSSH},
	}
}

func validPrivilege(p string) bool {
	switch p {
	case privRoot, privPackageOwner, privAgent, privUser:
		return true
	default:
		return false
	}
}

func scopeForPrivilege(p string) string {
	switch p {
	case privRoot:
		return scopeRoot
	case privPackageOwner, privAgent, privUser:
		return scopeUser
	default:
		return ""
	}
}

func (s OSString) forOS(osName string) string {
	if osName == osLinux {
		return s.Linux
	}
	return s.MacOS
}

func (l OSStringList) forOS(osName string) []string {
	if osName == osLinux {
		return l.Linux
	}
	return l.MacOS
}

// Validate checks a parsed unit catalog for duplicate IDs, invalid fields,
// dangling per-OS edges (depends, depends_if and waits_for) and per-OS
// dependency cycles.
func Validate(cat Catalog) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if len(cat.Units) == 0 {
		return []error{errors.New("units catalog defines no units")}
	}

	seen := make(map[string]int, len(cat.Units))
	for _, u := range cat.Units {
		if u.ID == "" {
			add("unit entry has empty id")
			continue
		}
		seen[u.ID]++
		if u.Domain == "" {
			add("unit %q has empty domain", u.ID)
		}
		if u.Epic == "" {
			add("unit %q has empty epic", u.ID)
		}
		if u.Milestone == "" {
			add("unit %q has empty milestone", u.ID)
		}
	}

	var dupIDs []string
	for id, count := range seen {
		if count > 1 {
			dupIDs = append(dupIDs, id)
		}
	}
	slices.Sort(dupIDs)
	for _, id := range dupIDs {
		add("duplicate unit id: %s", id)
	}

	barriers := knownBarriers()
	oses := []string{osLinux, osMacOS}

	for _, u := range cat.Units {
		for _, osName := range oses {
			st := u.Stage.forOS(osName)
			pr := u.Privilege.forOS(osName)
			if st == "" {
				add("%s: unit %s has empty stage", osName, u.ID)
			}
			if pr == "" {
				add("%s: unit %s has empty privilege", osName, u.ID)
			}
			if st == unsetMarker {
				if pr != unsetMarker {
					add("%s: unit %s has stage %q but privilege %q", osName, u.ID, st, pr)
				}
				if len(u.Depends.forOS(osName)) > 0 {
					add("%s: unit %s does not run on %s but declares depends", osName, u.ID, osName)
				}
				for _, cond := range sortedMapKeys(u.DependsIf) {
					if len(u.DependsIf[cond].forOS(osName)) > 0 {
						add("%s: unit %s does not run on %s but declares depends_if[%s]", osName, u.ID, osName, cond)
					}
				}
			} else if st != "" && !validPrivilege(pr) {
				add("%s: unit %s has unsupported privilege %q", osName, u.ID, pr)
			}
		}
		for _, cond := range sortedMapKeys(u.DependsIf) {
			if cond == "" {
				add("unit %s has empty depends_if condition", u.ID)
			}
		}
		for _, b := range u.WaitsFor {
			if _, ok := barriers[b]; !ok {
				add("unit %s waits_for unknown barrier %q", u.ID, b)
			}
		}
	}

	for _, osName := range oses {
		present := make(map[string]bool)
		for _, u := range cat.Units {
			if u.ID != "" && u.Stage.forOS(osName) != unsetMarker && u.Stage.forOS(osName) != "" {
				present[u.ID] = true
			}
		}

		edges := make(map[string][]string, len(present))
		for _, u := range cat.Units {
			if !present[u.ID] {
				continue
			}
			var deps []string
			for _, dep := range u.Depends.forOS(osName) {
				if !present[dep] {
					add("%s: %s depends on %s, which does not run on %s", osName, u.ID, dep, osName)
					continue
				}
				deps = append(deps, dep)
			}
			for _, cond := range sortedMapKeys(u.DependsIf) {
				for _, dep := range u.DependsIf[cond].forOS(osName) {
					if !present[dep] {
						add("%s: %s depends_if[%s] on %s, which does not run on %s", osName, u.ID, cond, dep, osName)
						continue
					}
					deps = append(deps, dep)
				}
			}
			for _, b := range u.WaitsFor {
				producers, ok := barriers[b]
				if !ok {
					continue
				}
				for _, prod := range producers {
					if !present[prod] {
						add("%s: %s waits_for %s, whose producer %s does not run on %s", osName, u.ID, b, prod, osName)
						continue
					}
					deps = append(deps, prod)
				}
			}
			edges[u.ID] = deps
		}

		errs = append(errs, findCycles(osName, edges)...)
	}

	return errs
}

func findCycles(osName string, edges map[string][]string) []error {
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)
	state := make(map[string]int, len(edges))
	var errs []error

	var visit func(node string, path []string)
	visit = func(node string, path []string) {
		switch state[node] {
		case visiting:
			start := slices.Index(path, node)
			if start < 0 {
				start = 0
			}
			cycle := append(slices.Clone(path[start:]), node)
			errs = append(errs, fmt.Errorf("%s: dependency cycle: %s", osName, strings.Join(cycle, " -> ")))
			return
		case visited:
			return
		}
		if _, ok := edges[node]; !ok {
			return
		}
		state[node] = visiting
		nextPath := append(slices.Clone(path), node)
		for _, dep := range edges[node] {
			visit(dep, nextPath)
		}
		state[node] = visited
	}

	for _, node := range sortedMapKeys(edges) {
		visit(node, nil)
	}
	return errs
}

// ValidatePresets checks that every unit ID named in registry/presets.toml
// exists in the unit catalog.
func ValidatePresets(cat Catalog, data []byte) []error {
	var doc presetsDoc
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return []error{fmt.Errorf("decode presets: %w", err)}
	}
	var errs []error
	if doc.SchemaVersion != 1 {
		errs = append(errs, fmt.Errorf("presets schema_version = %d, want 1", doc.SchemaVersion))
	}
	known := make(map[string]bool, len(cat.Units))
	for _, u := range cat.Units {
		known[u.ID] = true
	}
	checkGroup := func(group string, entries map[string]presetEntry) {
		for _, name := range sortedMapKeys(entries) {
			for _, id := range entries[name].Units {
				if !known[id] {
					errs = append(errs, fmt.Errorf("presets: %s.%s names unknown unit %s", group, name, id))
				}
			}
		}
	}
	checkGroup("tier", doc.Tier)
	checkGroup("purpose", doc.Purpose)
	return errs
}

// GeneratedFiles holds the formatted Go sources produced by the generator.
type GeneratedFiles struct {
	ContractEnums []byte
	CatalogTable  []byte
}

// Generate validates cat and renders all generated Go source files.
func Generate(cat Catalog) (GeneratedFiles, error) {
	if errs := Validate(cat); len(errs) > 0 {
		return GeneratedFiles{}, fmt.Errorf("validate units catalog: %w", errors.Join(errs...))
	}
	enums, err := RenderContractEnums(cat)
	if err != nil {
		return GeneratedFiles{}, err
	}
	table, err := RenderCatalog(cat)
	if err != nil {
		return GeneratedFiles{}, err
	}
	return GeneratedFiles{
		ContractEnums: enums,
		CatalogTable:  table,
	}, nil
}

// WriteRepo reads registry/units.toml (and registry/presets.toml if present)
// under repoRoot, validates them and writes the generated Go files.
func WriteRepo(repoRoot string) error {
	unitsPath := filepath.Join(repoRoot, "registry", "units.toml")
	unitsData, err := os.ReadFile(unitsPath) //nolint:gosec // path is caller-supplied repository root
	if err != nil {
		return fmt.Errorf("read %s: %w", unitsPath, err)
	}
	cat, err := ParseCatalog(unitsData)
	if err != nil {
		return err
	}
	presetsPath := filepath.Join(repoRoot, "registry", "presets.toml")
	if presetsData, err := os.ReadFile(presetsPath); err == nil { //nolint:gosec // path is caller-supplied repository root
		if errs := ValidatePresets(cat, presetsData); len(errs) > 0 {
			return fmt.Errorf("validate presets: %w", errors.Join(errs...))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", presetsPath, err)
	}

	out, err := Generate(cat)
	if err != nil {
		return err
	}

	enumsPath := filepath.Join(repoRoot, "internal", "contract", "enums_gen.go")
	if err := os.WriteFile(enumsPath, out.ContractEnums, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", enumsPath, err)
	}
	catalogPath := filepath.Join(repoRoot, "internal", "units", "catalog", "catalog_gen.go")
	if err := os.WriteFile(catalogPath, out.CatalogTable, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", catalogPath, err)
	}
	return nil
}

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
