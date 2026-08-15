package claude

import "embed"

//go:embed commands/*.md
var CommandFS embed.FS
