// Package crds carries the CustomResourceDefinitions avSYS needs, so the
// panel can install and upgrade them itself on startup.
package crds

import "embed"

//go:embed *.yaml
var Files embed.FS
