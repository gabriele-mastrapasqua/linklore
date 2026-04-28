// Package web embeds the templates and static assets at compile time so the
// linklore binary stays self-contained — no runtime path lookups.
package web

import "embed"

//go:embed templates/*.html templates/partials/*.html
var Templates embed.FS

//go:embed static/*
var Static embed.FS
