package http

import (
	"net/textproto"
	"sort"
	"strings"
)

// headerLogMode controls whether http request/response headers are printed
// to the console (works with -verbose, output goes through log.F).
// Valid values: "off", "request", "response", "all".
var headerLogMode = "off"

// SetHeaderLogMode sets the header logging mode.
// Any value other than request/response/all falls back to "off".
func SetHeaderLogMode(mode string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "request", "response", "all":
		headerLogMode = strings.ToLower(strings.TrimSpace(mode))
	default:
		headerLogMode = "off"
	}
}

// headerLogEnabled reports whether headers of the given direction
// ( "request" or "response" ) should be printed.
func headerLogEnabled(dir string) bool {
	return headerLogMode == "all" || headerLogMode == dir
}

// formatHeaders renders a readable, aligned text block of an http start
// line and its headers. Header keys are sorted for stable output.
func formatHeaders(title, client, startLine string, h textproto.MIMEHeader) string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("=== " + title + " ===\n")
	if client != "" {
		b.WriteString("    Client: " + client + "\n")
	}
	b.WriteString("    " + startLine + "\n")

	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		for _, v := range h[k] {
			b.WriteString("    " + k + ": " + v + "\n")
		}
	}

	b.WriteString("===========================\n")
	return b.String()
}
