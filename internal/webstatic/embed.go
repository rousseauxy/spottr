// Package webstatic embeds the compiled SvelteKit frontend (web/dist).
package webstatic

import "embed"

//go:embed all:dist
var FS embed.FS
