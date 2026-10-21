// Package web holds the public pages of the coordinator: the hive dashboard
// and the download page. download.html is self-contained, so it can also be
// copied into Vladimir's site as is.
package web

import "embed"

//go:embed *.html
var FS embed.FS
