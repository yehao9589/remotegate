package remotegate

import _ "embed"

// ReleaseNotes keeps the console's offline history and the published changelog
// on the same source, including ordinary go build and local development.
//
//go:embed CHANGELOG.md
var ReleaseNotes string
