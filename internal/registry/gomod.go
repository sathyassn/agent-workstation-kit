package registry

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"golang.org/x/mod/modfile"
)

// Kind is the kind of a tools registry entry.
type Kind string

// Registry entry kinds.
const (
	// KindGoLibrary is a direct require in go.mod.
	KindGoLibrary Kind = "go-library"
	// KindGoTool is a tool directive in go.mod.
	KindGoTool Kind = "go-tool"
)

// Tools is the decoded registry/tools.toml.
type Tools struct {
	SchemaVersion int     `toml:"schema_version"`
	Entries       []Entry `toml:"tool"`
}

// Entry is one pinned library or build tool.
type Entry struct {
	ID         string `toml:"id"`
	Kind       Kind   `toml:"kind"`
	Module     string `toml:"module"`
	ToolPath   string `toml:"tool_path"`
	Version    string `toml:"version"`
	Sum        string `toml:"sum"`
	Source     string `toml:"source"`
	Licence    string `toml:"licence"`
	LastReview string `toml:"last_review"`
	NextReview string `toml:"next_review"`
	Purpose    string `toml:"purpose"`
}

var (
	exactGoVersion  = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	exactToolchain  = regexp.MustCompile(`^go\d+\.\d+\.\d+$`)
	exactModVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+incompatible)?$`)
	h1Sum           = regexp.MustCompile(`^h1:[A-Za-z0-9+/]{43}=$`)
)

// CheckGoModules checks that go.mod is strictly parseable and exactly pinned,
// and that each direct require and tool directive has a registry entry whose
// version and h1 sum match go.mod and go.sum. Indirect modules are held by
// go.sum integrity and govulncheck, not by the registry. It returns every
// problem found.
func CheckGoModules(goMod, goSum, tools []byte) []error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	mf, err := modfile.Parse("go.mod", goMod, nil)
	if err != nil {
		return []error{fmt.Errorf("parse go.mod: %w", err)}
	}

	if mf.Go == nil || !exactGoVersion.MatchString(mf.Go.Version) {
		add("go directive must be an exact X.Y.Z version")
	}
	// go mod tidy drops a toolchain line equal to the go line, so an absent
	// toolchain is pinned by the exact go directive above.
	if mf.Toolchain != nil && !exactToolchain.MatchString(mf.Toolchain.Name) {
		add("toolchain %q is not an exact goX.Y.Z version", mf.Toolchain.Name)
	}
	for _, r := range mf.Replace {
		add("go.mod has a replace directive for %s; replace is not allowed", r.Old.Path)
	}
	for _, x := range mf.Exclude {
		add("go.mod has an exclude directive for %s; exclude is not allowed", x.Mod.Path)
	}

	all := make(map[string]string)
	direct := make(map[string]string)
	for _, r := range mf.Require {
		// modfile canonicalises shorthand such as v1.2, so check the version
		// as written too.
		written := writtenVersion(goMod, r)
		if !exactModVersion.MatchString(r.Mod.Version) || written != r.Mod.Version {
			add("module %s has non-exact version %q as written", r.Mod.Path, written)
		}
		all[r.Mod.Path] = r.Mod.Version
		if !r.Indirect {
			direct[r.Mod.Path] = r.Mod.Version
		}
	}
	if len(direct) == 0 {
		add("go.mod has no direct requires")
	}
	if len(mf.Tool) == 0 {
		add("go.mod has no tool directives")
	}

	sums, err := parseGoSum(goSum)
	if err != nil {
		return append(errs, err)
	}

	var reg Tools
	md, err := toml.Decode(string(tools), &reg)
	if err != nil {
		return append(errs, fmt.Errorf("decode registry/tools.toml: %w", err))
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		add("registry/tools.toml has unknown keys: %v", undecoded)
	}
	if reg.SchemaVersion != 1 {
		add("registry/tools.toml schema_version = %d, want 1", reg.SchemaVersion)
	}

	ids := make(map[string]bool)
	libs := make(map[string]Entry)
	toolsByPath := make(map[string]Entry)
	for _, e := range reg.Entries {
		errs = append(errs, checkEntry(e, ids)...)
		switch e.Kind {
		case KindGoLibrary:
			libs[e.Module] = e
		case KindGoTool:
			toolsByPath[e.ToolPath] = e
		}
	}

	for mod, ver := range direct {
		e, ok := libs[mod]
		if !ok {
			add("direct module %s %s has no go-library entry in registry/tools.toml", mod, ver)
			continue
		}
		errs = append(errs, checkPin(e, ver, sums)...)
	}
	for mod := range libs {
		if _, ok := direct[mod]; !ok {
			add("registry go-library %s is not a direct require in go.mod", mod)
		}
	}

	toolSet := make(map[string]bool)
	for _, t := range mf.Tool {
		toolSet[t.Path] = true
		e, ok := toolsByPath[t.Path]
		if !ok {
			add("tool %s has no go-tool entry in registry/tools.toml", t.Path)
			continue
		}
		owner, ok := owningModule(t.Path, all)
		if !ok {
			add("tool %s is not inside any module required in go.mod", t.Path)
			continue
		}
		if e.Module != owner {
			add("tool %s belongs to module %s, but its registry entry names %s", t.Path, owner, e.Module)
			continue
		}
		ver := all[owner]
		errs = append(errs, checkPin(e, ver, sums)...)
	}
	for p := range toolsByPath {
		if !toolSet[p] {
			add("registry go-tool %s is not a tool directive in go.mod", p)
		}
	}
	return errs
}

// owningModule returns the longest required module path that equals the
// package path or is a whole-element prefix of it, as Go resolves packages.
func owningModule(pkg string, required map[string]string) (string, bool) {
	best := ""
	for mod := range required {
		if (pkg == mod || strings.HasPrefix(pkg, mod+"/")) && len(mod) > len(best) {
			best = mod
		}
	}
	return best, best != ""
}

func checkEntry(e Entry, ids map[string]bool) []error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if e.ID == "" {
		add("registry entry for module %q has no id", e.Module)
	}
	if ids[e.ID] {
		add("duplicate registry id %q", e.ID)
	}
	ids[e.ID] = true
	switch e.Kind {
	case KindGoLibrary:
		if e.ToolPath != "" {
			add("go-library %q must not set tool_path", e.ID)
		}
	case KindGoTool:
		if e.ToolPath == "" {
			add("go-tool %q has no tool_path", e.ID)
		}
	default:
		add("registry entry %q has unsupported kind %q", e.ID, e.Kind)
	}
	if !exactModVersion.MatchString(e.Version) {
		add("registry entry %q has non-exact version %q", e.ID, e.Version)
	}
	if !h1Sum.MatchString(e.Sum) {
		add("registry entry %q has no valid h1 sum", e.ID)
	}
	if !strings.HasPrefix(e.Source, "https://") {
		add("registry entry %q has non-https source %q", e.ID, e.Source)
	}
	if e.Licence == "" {
		add("registry entry %q has no licence", e.ID)
	}
	last, errLast := time.Parse(time.DateOnly, e.LastReview)
	next, errNext := time.Parse(time.DateOnly, e.NextReview)
	if errLast != nil {
		add("registry entry %q has invalid last_review %q", e.ID, e.LastReview)
	}
	if errNext != nil {
		add("registry entry %q has invalid next_review %q", e.ID, e.NextReview)
	}
	if errLast == nil && errNext == nil && !next.After(last) {
		add("registry entry %q next_review is not after last_review", e.ID)
	}
	if e.Purpose == "" {
		add("registry entry %q has no purpose", e.ID)
	}
	return errs
}

func checkPin(e Entry, goModVersion string, sums map[string]string) []error {
	var errs []error
	if e.Version != goModVersion {
		errs = append(errs, fmt.Errorf("%s version mismatch: go.mod=%s registry=%s", e.Module, goModVersion, e.Version))
	}
	got, ok := sums[e.Module+" "+goModVersion]
	switch {
	case !ok:
		errs = append(errs, fmt.Errorf("go.sum has no h1 sum for %s %s", e.Module, goModVersion))
	case got != e.Sum:
		errs = append(errs, fmt.Errorf("%s %s sum mismatch: go.sum=%s registry=%s", e.Module, goModVersion, got, e.Sum))
	}
	return errs
}

// writtenVersion returns the version of a require line exactly as it appears
// in the go.mod bytes, before modfile canonicalises it.
func writtenVersion(goMod []byte, r *modfile.Require) string {
	if r.Syntax == nil {
		return ""
	}
	start, end := r.Syntax.Start.Byte, r.Syntax.End.Byte
	if start < 0 || end > len(goMod) || start >= end {
		return ""
	}
	code, _, _ := strings.Cut(string(goMod[start:end]), "//")
	fields := strings.Fields(code)
	for i, f := range fields {
		if f == r.Mod.Path && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// parseGoSum maps "module version" to the module zip h1 sum. The /go.mod
// lines are skipped. Malformed lines and conflicting sums are errors.
func parseGoSum(data []byte) (map[string]string, error) {
	sums := make(map[string]string)
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 || !h1Sum.MatchString(f[2]) {
			return nil, fmt.Errorf("go.sum line %d is malformed", n)
		}
		if strings.HasSuffix(f[1], "/go.mod") {
			continue
		}
		key := f[0] + " " + f[1]
		if prev, ok := sums[key]; ok && prev != f[2] {
			return nil, fmt.Errorf("go.sum has conflicting sums for %s", key)
		}
		sums[key] = f[2]
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read go.sum: %w", err)
	}
	if len(sums) == 0 {
		return nil, errors.New("go.sum has no module sums")
	}
	return sums, nil
}
