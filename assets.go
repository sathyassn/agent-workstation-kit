// Package wskit embeds the repository's static data folders so internal
// packages can read schemas, registries, dotfiles and templates from the
// compiled binary.
package wskit

import (
	"embed"
	"io/fs"
)

//go:embed all:schema
var schemaFS embed.FS

//go:embed all:registry
var registryFS embed.FS

//go:embed all:dotfiles
var dotfilesFS embed.FS

//go:embed all:templates
var templatesFS embed.FS

// SchemaFS returns the embedded schema/ directory tree.
func SchemaFS() fs.FS {
	return schemaFS
}

// RegistryFS returns the embedded registry/ directory tree.
func RegistryFS() fs.FS {
	return registryFS
}

// DotfilesFS returns the embedded dotfiles/ directory tree.
func DotfilesFS() fs.FS {
	return dotfilesFS
}

// TemplatesFS returns the embedded templates/ directory tree.
func TemplatesFS() fs.FS {
	return templatesFS
}
