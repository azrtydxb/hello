// Package hello embeds the repository-level files the binaries read at run
// time: momo.yaml, the momo settings that internal/migrate gives the
// migration engine, so hello-control and the momo command line run one
// configuration.
package hello

import "embed"

// MomoConfig holds momo.yaml at its root.
//
//go:embed momo.yaml
var MomoConfig embed.FS
