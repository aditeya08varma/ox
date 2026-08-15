// Package skills owns ox's canonical Agent Skills source tree. Adapters install
// these unchanged into their discovery roots; host-specific behavior belongs in
// the adapter, not in a fork of the skill body.
package skills

import "embed"

//go:embed all:*
var FS embed.FS
