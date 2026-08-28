package web

import (
	"net/http"
	"testing"
)

func TestServeIndex(t *testing.T) {
	status, headers, body := Serve("/index.html")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := headers.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("content-type = %q, want text/html", got)
	}
	if len(body) == 0 {
		t.Fatal("body is empty")
	}
	if string(body[:min(len(body), 15)]) == "" {
		t.Fatal("body not a string")
	}
	// Embedded file should be an HTML document.
	if !contains(body, "<!doctype html>") && !contains(body, "<!DOCTYPE html>") {
		t.Fatalf("body does not look like html: %s", string(body[:40]))
	}
}

func TestServeTrailingSlashTrimmed(t *testing.T) {
	status, _, _ := Serve("/index.html/")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (trailing slash trimmed)", status)
	}
}

func TestServeQuotaPageUsesStrictSecurityHeaders(t *testing.T) {
	status, headers, body := Serve(QuotaPath)
	if status != http.StatusOK || len(body) == 0 {
		t.Fatalf("quota page response = %d, %d bytes", status, len(body))
	}
	if headers.Get("Cache-Control") != "no-store" || headers.Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Fatalf("quota page security headers = %+v", headers)
	}
	csp := headers.Get("Content-Security-Policy")
	if csp == "" || contains([]byte(csp), "raw.githubusercontent.com") {
		t.Fatalf("quota page CSP = %q, want same-origin-only connect-src", csp)
	}
}

func TestServeDoesNotExposeQuotaAPIsAsHTML(t *testing.T) {
	for _, path := range []string{QuotaAPIPath, QuotaResetAPIPath} {
		status, _, _ := Serve(path)
		if status != http.StatusNotFound {
			t.Fatalf("quota API %s static status = %d, want 404", path, status)
		}
	}
}

func TestServeUnknownPath404(t *testing.T) {
	status, _, _ := Serve("/assets/app.js")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

func contains(haystack []byte, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, []byte(needle)) >= 0
}

func indexOf(h, n []byte) int {
outer:
	for i := 0; i <= len(h)-len(n); i++ {
		for j := 0; j < len(n); j++ {
			if h[i+j] != n[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
