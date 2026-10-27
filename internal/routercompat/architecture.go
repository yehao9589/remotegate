package routercompat

import (
	_ "embed"
	"strings"
)

//go:embed architecture.sh
var architectureRaw string

// ArchitectureShell is shared by downloaded and bundled installers.
// Normalize Windows checkouts before sending the script to a POSIX shell.
var ArchitectureShell = strings.ReplaceAll(architectureRaw, "\r\n", "\n")
