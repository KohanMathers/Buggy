// Package web holds buggy's HTML templates and static assets.
package web

import "embed"

//go:embed templates static
var FS embed.FS
