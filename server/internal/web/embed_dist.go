//go:build webdist

package web

import (
	"embed"
	"io/fs"
)

// С тегом webdist вшивается собранное приложение из apps/web (pnpm -F web build).
//
//go:embed all:dist
var files embed.FS

func assets() fs.FS {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
