package static

import "embed"

// FS holds the embedded frontend files
// The web/dist directory must exist when building
// Build frontend first: cd web && npm run build
//
//go:embed all:dist
var FS embed.FS
