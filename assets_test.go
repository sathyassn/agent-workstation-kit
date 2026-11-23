package wskit_test

import (
	"io/fs"
	"os"
	"testing"

	// Pin direct dependencies declared in SPC-001 / ADR 0005 section 2b so
	// `go mod tidy` retains them until unit packages land in EPC-003/EPC-004.
	_ "charm.land/bubbles/v2/spinner"
	_ "charm.land/bubbletea/v2"
	_ "charm.land/fang/v2"
	_ "charm.land/glamour/v2"
	_ "charm.land/huh/v2"
	_ "charm.land/lipgloss/v2"
	_ "github.com/BurntSushi/toml"
	_ "github.com/gowebpki/jcs"
	_ "github.com/spf13/cobra"
	_ "golang.org/x/sys/unix"

	wskit "github.com/sathyassn/agent-workstation-kit"
	"github.com/sathyassn/agent-workstation-kit/internal/registry"
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

func TestGoModAndToolsRegistry(t *testing.T) {
	t.Parallel()

	goMod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	goSum, err := os.ReadFile("go.sum")
	if err != nil {
		t.Fatalf("read go.sum: %v", err)
	}
	tools, err := fs.ReadFile(wskit.RegistryFS(), "registry/tools.toml")
	if err != nil {
		t.Fatalf("read registry/tools.toml: %v", err)
	}
	for _, problem := range registry.CheckGoModules(goMod, goSum, tools) {
		t.Error(problem)
	}
}
