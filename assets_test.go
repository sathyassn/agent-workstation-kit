package wskit_test

import (
	"bufio"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	// Pin direct dependencies declared in SPC-001 / ADR 0005 section 2b so
	// `go mod tidy` retains them until unit packages land in EPC-003/EPC-004.
	_ "charm.land/bubbles/v2/spinner"
	_ "charm.land/bubbletea/v2"
	_ "charm.land/fang/v2"
	_ "charm.land/glamour/v2"
	_ "charm.land/huh/v2"
	_ "charm.land/lipgloss/v2"
	_ "github.com/gowebpki/jcs"
	_ "github.com/spf13/cobra"
	_ "golang.org/x/sys/unix"

	wskit "github.com/sathyassn/agent-workstation-kit"
)

func TestEmbeddedAssets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		fsys fs.FS
		path string
	}{
		{name: "schema", fsys: wskit.SchemaFS(), path: "schema/README.md"},
		{name: "registry_readme", fsys: wskit.RegistryFS(), path: "registry/README.md"},
		{name: "registry_tools", fsys: wskit.RegistryFS(), path: "registry/tools.toml"},
		{name: "registry_units", fsys: wskit.RegistryFS(), path: "registry/units.toml"},
		{name: "dotfiles_readme", fsys: wskit.DotfilesFS(), path: "dotfiles/README.md"},
		{name: "dotfiles_plugins", fsys: wskit.DotfilesFS(), path: "dotfiles/zsh_plugins.txt"},
		{name: "templates_readme", fsys: wskit.TemplatesFS(), path: "templates/README.md"},
		{name: "templates_gitignore", fsys: wskit.TemplatesFS(), path: "templates/private-fleet/.gitignore"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data, err := fs.ReadFile(tc.fsys, tc.path)
			if err != nil {
				t.Fatalf("read embedded %s: %v", tc.path, err)
			}
			if len(data) == 0 {
				t.Fatalf("embedded %s is empty", tc.path)
			}
		})
	}
}

type toolsRegistry struct {
	SchemaVersion int         `toml:"schema_version"`
	Tools         []toolEntry `toml:"tool"`
}

type toolEntry struct {
	ID         string `toml:"id"`
	Kind       string `toml:"kind"`
	Module     string `toml:"module"`
	ToolPath   string `toml:"tool_path"`
	Version    string `toml:"version"`
	Source     string `toml:"source"`
	Licence    string `toml:"licence"`
	LastReview string `toml:"last_review"`
	NextReview string `toml:"next_review"`
	Purpose    string `toml:"purpose"`
}

type parsedGoMod struct {
	goVersion string
	toolchain string
	direct    map[string]string
	all       map[string]string
	tools     []string
}

var (
	exactGoVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	exactToolchainPattern = regexp.MustCompile(`^go\d+\.\d+\.\d+$`)
	exactModVerPattern    = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+incompatible)?$`)
)

func parseGoMod(t *testing.T) parsedGoMod {
	t.Helper()

	f, err := os.Open("go.mod")
	if err != nil {
		t.Fatalf("open go.mod: %v", err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			t.Errorf("close go.mod: %v", closeErr)
		}
	}()

	res := parsedGoMod{
		direct: make(map[string]string),
		all:    make(map[string]string),
	}

	var block string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" || strings.HasPrefix(raw, "//") {
			continue
		}
		if raw == ")" {
			block = ""
			continue
		}

		if block == "" {
			switch {
			case strings.HasPrefix(raw, "go "):
				res.goVersion = strings.TrimSpace(strings.TrimPrefix(raw, "go "))
			case strings.HasPrefix(raw, "toolchain "):
				res.toolchain = strings.TrimSpace(strings.TrimPrefix(raw, "toolchain "))
			case raw == "require (":
				block = "require"
			case raw == "tool (":
				block = "tool"
			case strings.HasPrefix(raw, "require "):
				recordRequire(&res, strings.TrimSpace(strings.TrimPrefix(raw, "require ")))
			case strings.HasPrefix(raw, "tool "):
				res.tools = append(res.tools, strings.TrimSpace(strings.TrimPrefix(raw, "tool ")))
			}
			continue
		}

		switch block {
		case "require":
			recordRequire(&res, raw)
		case "tool":
			fields := strings.Fields(raw)
			if len(fields) > 0 {
				res.tools = append(res.tools, fields[0])
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan go.mod: %v", err)
	}
	return res
}

func recordRequire(res *parsedGoMod, line string) {
	code, comment, _ := strings.Cut(line, "//")
	fields := strings.Fields(code)
	if len(fields) < 2 {
		return
	}
	modPath, version := fields[0], fields[1]
	res.all[modPath] = version
	if !strings.Contains(comment, "indirect") {
		res.direct[modPath] = version
	}
}

func TestGoModAndToolsRegistry(t *testing.T) {
	t.Parallel()

	gm := parseGoMod(t)

	if !exactGoVersionPattern.MatchString(gm.goVersion) {
		t.Fatalf("go directive %q is not an exact X.Y.Z version", gm.goVersion)
	}
	if gm.toolchain != "" && !exactToolchainPattern.MatchString(gm.toolchain) {
		t.Fatalf("toolchain directive %q is not pinned to an exact goX.Y.Z version", gm.toolchain)
	}
	for mod, ver := range gm.all {
		if !exactModVerPattern.MatchString(ver) {
			t.Errorf("module %s has non-exact version %q", mod, ver)
		}
	}
	if len(gm.direct) == 0 {
		t.Fatal("go.mod has no direct require entries")
	}
	if len(gm.tools) == 0 {
		t.Fatal("go.mod has no tool entries")
	}

	raw, err := fs.ReadFile(wskit.RegistryFS(), "registry/tools.toml")
	if err != nil {
		t.Fatalf("read registry/tools.toml: %v", err)
	}

	var reg toolsRegistry
	md, err := toml.Decode(string(raw), &reg)
	if err != nil {
		t.Fatalf("decode registry/tools.toml: %v", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		t.Fatalf("registry/tools.toml has unknown keys: %v", undecoded)
	}
	if reg.SchemaVersion != 1 {
		t.Fatalf("registry/tools.toml schema_version = %d, want 1", reg.SchemaVersion)
	}

	seenIDs := make(map[string]bool)
	libsByMod := make(map[string]toolEntry)
	toolsByPath := make(map[string]toolEntry)

	for _, entry := range reg.Tools {
		if entry.ID == "" {
			t.Errorf("tool entry with module %q has empty id", entry.Module)
		}
		if seenIDs[entry.ID] {
			t.Errorf("duplicate tool id %q in registry/tools.toml", entry.ID)
		}
		seenIDs[entry.ID] = true

		if !exactModVerPattern.MatchString(entry.Version) {
			t.Errorf("tool %q has non-exact version %q", entry.ID, entry.Version)
		}
		if !strings.HasPrefix(entry.Source, "https://") {
			t.Errorf("tool %q has non-https source %q", entry.ID, entry.Source)
		}
		if entry.Licence == "" {
			t.Errorf("tool %q has empty licence", entry.ID)
		}
		if _, err := time.Parse("2006-01-02", entry.LastReview); err != nil {
			t.Errorf("tool %q has invalid last_review %q: %v", entry.ID, entry.LastReview, err)
		}
		if _, err := time.Parse("2006-01-02", entry.NextReview); err != nil {
			t.Errorf("tool %q has invalid next_review %q: %v", entry.ID, entry.NextReview, err)
		}
		if entry.Purpose == "" {
			t.Errorf("tool %q has empty purpose", entry.ID)
		}

		switch entry.Kind {
		case "go-library":
			libsByMod[entry.Module] = entry
		case "go-tool":
			if entry.ToolPath == "" {
				t.Errorf("go-tool %q has empty tool_path", entry.ID)
			}
			toolsByPath[entry.ToolPath] = entry
		default:
			t.Errorf("tool %q has unsupported kind %q", entry.ID, entry.Kind)
		}
	}

	for mod, ver := range gm.direct {
		entry, ok := libsByMod[mod]
		if !ok {
			t.Errorf("direct module %s (%s) in go.mod has no go-library entry in registry/tools.toml", mod, ver)
			continue
		}
		if entry.Version != ver {
			t.Errorf("module %s version mismatch: go.mod=%s registry=%s", mod, ver, entry.Version)
		}
	}
	for mod := range libsByMod {
		if _, ok := gm.direct[mod]; !ok {
			t.Errorf("registry/tools.toml lists go-library %s which is not a direct require in go.mod", mod)
		}
	}

	toolSet := make(map[string]bool, len(gm.tools))
	for _, tp := range gm.tools {
		toolSet[tp] = true
		entry, ok := toolsByPath[tp]
		if !ok {
			t.Errorf("go tool %s in go.mod has no go-tool entry in registry/tools.toml", tp)
			continue
		}
		ver, ok := gm.all[entry.Module]
		if !ok {
			t.Errorf("go tool %s module %s not found in go.mod require list", tp, entry.Module)
			continue
		}
		if entry.Version != ver {
			t.Errorf("go tool %s (%s) version mismatch: go.mod=%s registry=%s", tp, entry.Module, ver, entry.Version)
		}
	}
	for tp := range toolsByPath {
		if !toolSet[tp] {
			t.Errorf("registry/tools.toml lists go-tool %s which is not a tool directive in go.mod", tp)
		}
	}
}
