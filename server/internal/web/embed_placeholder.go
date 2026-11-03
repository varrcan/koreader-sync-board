//go:build !webdist

package web

import (
	"embed"
	"io/fs"
)

// Сборка без тега webdist (чистый клон, без Node) вшивает страницу-заглушку.
//
//go:embed placeholder
var files embed.FS

func assets() fs.FS {
	sub, err := fs.Sub(files, "placeholder")
	if err != nil {
		panic(err)
	}
	return sub
}
