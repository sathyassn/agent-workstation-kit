// Package gen parses, validates and generates Go code from registry/units.toml.
package gen

import (
	"bytes"
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
	SchemaVersion int                      `toml:"schema_version"`
	Tier          map[string]tierPreset    `toml:"tier"`
	Purpose       map[string]purposePreset `toml:"purpose"`
	Defaults      presetDefaults           `toml:"defaults"`
}

type tierPreset struct {
	Extends     string   `toml:"extends"`
	AllFeasible bool     `toml:"all_feasible"`
	Units       []string `toml:"units"`
}

type purposePreset struct {
	Topology     string   `toml:"topology"`
	RemoteAccess bool     `toml:"remote_access"`
	Units        []string `toml:"units"`
}

type presetDefaults struct {
	Resources  resourceDefaults              `toml:"resources"`
	FormFactor map[string]formFactorDefaults `toml:"form_factor"`
	Indexing   indexingDefaults              `toml:"indexing"`
}

type resourceDefaults struct {
	MemoryHighPercent    int    `toml:"memory_high_percent"`
	MemoryMaxPercent     int    `toml:"memory_max_percent"`
	SwapMaxPercent       int    `toml:"swap_max_percent"`
	MemoryReservePercent int    `toml:"memory_reserve_percent"`
	OOMDPressurePercent  int    `toml:"oomd_pressure_percent"`
	Swappiness           any    `toml:"swappiness"`
	ZramPercent          int    `toml:"zram_percent"`
	Tmp                  string `toml:"tmp"`
	DiskFreeFloorPercent int    `toml:"disk_free_floor_percent"`
}

type formFactorDefaults struct {
	RemoteAccess     *bool  `toml:"remote_access"`
	ZramPercent      *int   `toml:"zram_percent"`
	DiskEncryption   string `toml:"disk_encryption"`
	AlwaysOnServices *bool  `toml:"always_on_services"`
	RoamingFirewall  *bool  `toml:"roaming_firewall"`
}

type indexingDefaults struct {
	MacOSDevelopmentVolumes string `toml:"macos_development_volumes"`
	KeepSpotlight           bool   `toml:"keep_spotlight"`
}

// isValidToken reports whether s matches ^[a-z][a-z0-9]*(-[a-z0-9]+)*$.
func isValidToken(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == '-' {
			if i == len(s)-1 || s[i-1] == '-' {
				return false
			}
			continue
		}
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
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
		if !isValidToken(u.ID) {
			add("unit id %q is not a valid identifier", u.ID)
		}
		seen[u.ID]++
		if u.Domain == "" {
			add("unit %q has empty domain", u.ID)
		} else if !isValidToken(u.Domain) {
			add("unit %q has invalid domain %q", u.ID, u.Domain)
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
			} else if st != "" {
				if !isValidToken(st) {
					add("%s: unit %s has invalid stage %q", osName, u.ID, st)
				}
				if pr != "" && pr != unsetMarker && !isValidToken(pr) {
					add("%s: unit %s has invalid privilege %q", osName, u.ID, pr)
				}
				if !validPrivilege(pr) {
					add("%s: unit %s has unsupported privilege %q", osName, u.ID, pr)
				} else if sc := scopeForPrivilege(pr); !isValidToken(sc) {
					add("%s: unit %s has invalid scope %q", osName, u.ID, sc)
				}
			}
		}
		for _, cond := range sortedMapKeys(u.DependsIf) {
			if cond == "" {
				add("unit %s has empty depends_if condition", u.ID)
			} else if !isValidToken(cond) {
				add("unit %s has invalid depends_if condition %q", u.ID, cond)
			}
		}
		for _, b := range u.WaitsFor {
			if !isValidToken(b) {
				add("unit %s has invalid waits_for barrier %q", u.ID, b)
			}
			if _, ok := barriers[b]; !ok {
				add("unit %s waits_for unknown barrier %q", u.ID, b)
			}
		}
	}

	checkCollisions := func(kind, prefix string, values []string) {
		byIdent := make(map[string]string, len(values))
		for _, v := range values {
			if v == "" || v == unsetMarker {
				continue
			}
			ident := goIdent(prefix, v)
			if prev, exists := byIdent[ident]; exists && prev != v {
				add("%s %q and %q collide on Go identifier %s", kind, prev, v, ident)
			} else if !exists {
				byIdent[ident] = v
			}
		}
	}
	checkCollisions("unit id", "Unit", collectUnitIDs(cat))
	checkCollisions("stage", typeStage, collectStages(cat))
	checkCollisions("scope", typeScope, collectScopes(cat))
	checkCollisions("domain", typeDomain, collectDomains(cat))
	checkCollisions("privilege", typePrivilege, collectPrivileges(cat))
	checkCollisions("condition", "Condition", collectConditions(cat))
	checkCollisions("barrier", "Barrier", collectBarriers(cat))

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

// ValidatePresets checks that registry/presets.toml has no unknown keys, uses
// schema_version 1, and names only unit IDs present in the unit catalog.
func ValidatePresets(cat Catalog, data []byte) []error {
	var doc presetsDoc
	md, err := toml.Decode(string(data), &doc)
	if err != nil {
		return []error{fmt.Errorf("decode presets: %w", err)}
	}
	var errs []error
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		errs = append(errs, fmt.Errorf("presets has unknown keys: %v", undecoded))
	}
	if doc.SchemaVersion != 1 {
		errs = append(errs, fmt.Errorf("presets schema_version = %d, want 1", doc.SchemaVersion))
	}
	known := make(map[string]bool, len(cat.Units))
	for _, u := range cat.Units {
		known[u.ID] = true
	}
	for _, name := range sortedMapKeys(doc.Tier) {
		for _, id := range doc.Tier[name].Units {
			if !known[id] {
				errs = append(errs, fmt.Errorf("presets: tier.%s names unknown unit %s", name, id))
			}
		}
	}
	for _, name := range sortedMapKeys(doc.Purpose) {
		for _, id := range doc.Purpose[name].Units {
			if !known[id] {
				errs = append(errs, fmt.Errorf("presets: purpose.%s names unknown unit %s", name, id))
			}
		}
	}
	return errs
}

// GeneratedFiles holds the formatted Go sources and Markdown table produced by
// the generator.
type GeneratedFiles struct {
	ContractEnums []byte
	CatalogTable  []byte
	DocsTable     string
}

// Generate validates cat and renders all generated Go source files and the
// Markdown unit table.
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
		DocsTable:     RenderDocsTable(cat),
	}, nil
}

func loadAndGenerateRepo(repoRoot string) (GeneratedFiles, error) {
	unitsPath := filepath.Join(repoRoot, "registry", "units.toml")
	unitsData, err := os.ReadFile(unitsPath) //nolint:gosec // path is caller-supplied repository root
	if err != nil {
		return GeneratedFiles{}, fmt.Errorf("read %s: %w", unitsPath, err)
	}
	cat, err := ParseCatalog(unitsData)
	if err != nil {
		return GeneratedFiles{}, err
	}
	presetsPath := filepath.Join(repoRoot, "registry", "presets.toml")
	if presetsData, err := os.ReadFile(presetsPath); err == nil { //nolint:gosec // path is caller-supplied repository root
		if errs := ValidatePresets(cat, presetsData); len(errs) > 0 {
			return GeneratedFiles{}, fmt.Errorf("validate presets: %w", errors.Join(errs...))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return GeneratedFiles{}, fmt.Errorf("read %s: %w", presetsPath, err)
	}
	return Generate(cat)
}

type docUpdate struct {
	relPath string
	absPath string
	updated []byte
	stale   bool
}

func planDocUpdates(repoRoot, docsTable string) ([]docUpdate, error) {
	var updates []docUpdate
	for _, rel := range DocTargets() {
		abs := filepath.Join(repoRoot, filepath.FromSlash(rel))
		data, err := os.ReadFile(abs) //nolint:gosec // path is inside caller-supplied repository root
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", rel, err)
		}
		if !bytes.Contains(data, []byte(docsBeginMarker)) || !bytes.Contains(data, []byte(docsEndMarker)) {
			continue
		}
		updated, err := ReplaceDocsTable(data, docsTable)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", rel, err)
		}
		updates = append(updates, docUpdate{
			relPath: rel,
			absPath: abs,
			updated: updated,
			stale:   !bytes.Equal(data, updated),
		})
	}
	return updates, nil
}

// WriteRepo reads registry/units.toml (and registry/presets.toml if present)
// under repoRoot, validates them, type-checks the generated Go sources against
// the repository's contract and catalog packages, and writes the generated Go
// files and Markdown unit tables only after all checks succeed.
func WriteRepo(repoRoot string) error {
	out, err := loadAndGenerateRepo(repoRoot)
	if err != nil {
		return err
	}

	contractDir := filepath.Join(repoRoot, "internal", "contract")
	catalogDir := filepath.Join(repoRoot, "internal", "units", "catalog")
	extraContract, err := readHandwrittenGoFiles(contractDir)
	if err != nil {
		return err
	}
	extraCatalog, err := readHandwrittenGoFiles(catalogDir)
	if err != nil {
		return err
	}
	if err := TypeCheckSources(out.ContractEnums, out.CatalogTable, extraContract, extraCatalog); err != nil {
		return err
	}

	docUpdates, err := planDocUpdates(repoRoot, out.DocsTable)
	if err != nil {
		return err
	}

	enumsPath := filepath.Join(contractDir, "enums_gen.go")
	if err := os.WriteFile(enumsPath, out.ContractEnums, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", enumsPath, err)
	}
	catalogPath := filepath.Join(catalogDir, "catalog_gen.go")
	if err := os.WriteFile(catalogPath, out.CatalogTable, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", catalogPath, err)
	}
	for _, upd := range docUpdates {
		if !upd.stale {
			continue
		}
		if err := os.WriteFile(upd.absPath, upd.updated, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", upd.relPath, err)
		}
	}
	return nil
}

// CheckRepo verifies that registry/units.toml, registry/presets.toml, the
// generated Go files and every present Markdown catalog table under repoRoot
// are valid and up to date.
func CheckRepo(repoRoot string) error {
	out, err := loadAndGenerateRepo(repoRoot)
	if err != nil {
		return err
	}

	var errs []error
	checkFile := func(rel string, want []byte) {
		abs := filepath.Join(repoRoot, filepath.FromSlash(rel))
		got, readErr := os.ReadFile(abs) //nolint:gosec // path is inside caller-supplied repository root
		if readErr != nil {
			errs = append(errs, fmt.Errorf("read %s: %w", rel, readErr))
			return
		}
		if !bytes.Equal(got, want) {
			errs = append(errs, fmt.Errorf("%s is stale; run `go generate ./...`", rel))
		}
	}

	checkFile("internal/contract/enums_gen.go", out.ContractEnums)
	checkFile("internal/units/catalog/catalog_gen.go", out.CatalogTable)

	docUpdates, err := planDocUpdates(repoRoot, out.DocsTable)
	if err != nil {
		errs = append(errs, err)
	} else {
		for _, upd := range docUpdates {
			if upd.stale {
				errs = append(errs, fmt.Errorf("%s is stale; run `go generate ./...`", upd.relPath))
			}
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
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
