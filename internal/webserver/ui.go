package webserver

import "embed"

// uiFiles is the dashboard UI: plain HTML, JS and CSS, no build step.
//
//go:embed ui
var uiFiles embed.FS
