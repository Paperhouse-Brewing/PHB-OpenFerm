// SPDX-License-Identifier: Apache-2.0
package web

import (
	"embed"
	"io/fs"
	"os"
)

//go:embed templates/*.html static/*
var Embedded embed.FS

// SelectFS prefers /var/lib/openbrew/www (if present) for live edits,
// otherwise falls back to the embedded assets.
func SelectFS() fs.FS {
	const override = "/var/lib/openbrew/www"
	if st, err := os.Stat(override); err == nil && st.IsDir() {
		return os.DirFS(override)
	}
	return Embedded
}

func UsingOverride() bool {
	const override = "/var/lib/openbrew/www"
	if st, err := os.Stat(override); err == nil && st.IsDir() {
		return true
	}
	return false
}
