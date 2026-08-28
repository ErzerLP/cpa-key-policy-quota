// Package web embeds the built management UI (a single inlined index.html)
// and serves it as a CPA plugin resource under
// /v0/resource/plugins/cpa-key-policy/index.html.
//
// dist/index.html is a build artifact produced by `npm run build` in ../../web.
// A placeholder is committed so the Go build never fails when the frontend has
// not been built yet; the real UI replaces it after a frontend build.
package web

import (
	_ "embed"
	"net/http"
	"strings"
)

//go:embed dist/index.html
var indexHTML []byte

const contentType = "text/html; charset=utf-8"

// IndexPath is the resource path (relative to the plugin resource base) for
// the management UI. QuotaPath serves the same single-file bundle in
// self-service mode, while the API paths are handled by the plugin backend.
const (
	IndexPath         = "/index.html"
	QuotaPath         = "/quota.html"
	QuotaAPIPath      = "/quota/api"
	QuotaResetAPIPath = "/quota/api/reset"
)

// Serve returns the embedded single-file UI for either browser entry point.
func Serve(path string) (status int, headers http.Header, body []byte) {
	clean := strings.TrimRight(path, "/")
	if clean != IndexPath && clean != QuotaPath {
		return http.StatusNotFound, http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}}, []byte("not found")
	}
	headers = http.Header{
		"Content-Type":            []string{contentType},
		"Cache-Control":           []string{"no-store"},
		"Content-Security-Policy": []string{resourceCSP(clean)},
		"Permissions-Policy":      []string{"camera=(), microphone=(), geolocation=()"},
		"Referrer-Policy":         []string{"no-referrer"},
		"X-Content-Type-Options":  []string{"nosniff"},
		"X-Frame-Options":         []string{"SAMEORIGIN"},
	}
	return http.StatusOK, headers, indexHTML
}

func resourceCSP(path string) string {
	connectSources := "'self'"
	if path == IndexPath {
		connectSources += " https://raw.githubusercontent.com"
	}
	return "default-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'self'; " +
		"script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data:; " +
		"font-src 'self' data:; connect-src " + connectSources
}
