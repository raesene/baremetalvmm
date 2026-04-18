package web

import "embed"

//go:embed templates/*.html static/*
var Assets embed.FS
