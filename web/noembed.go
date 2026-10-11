//go:build !embedweb

// Package web carries the built web UI.
package web

import "io/fs"

// Dist is the built web UI, or nil in binaries built without it.
var Dist fs.FS
