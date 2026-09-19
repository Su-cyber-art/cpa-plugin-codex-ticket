package ticket

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"net/http"
	"regexp"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

//go:embed ui.html
var settingsHTML []byte

func pageResponse() []byte {
	// Only allow this exact embedded script; no external scripts or user HTML.
	policy := "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'; connect-src 'self'; img-src 'self' data:; style-src 'unsafe-inline'; script-src"
	for _, m := range regexp.MustCompile(`(?s)<script(?:\s[^>]*)?>(.*?)</script>`).FindAllSubmatch(settingsHTML, -1) {
		sum := sha256.Sum256(m[1])
		policy += " 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	}
	return envelope(pluginapi.ManagementResponse{StatusCode: 200, Headers: http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Cache-Control": {"no-store"}, "Content-Security-Policy": {policy}, "X-Content-Type-Options": {"nosniff"}, "Referrer-Policy": {"no-referrer"}}, Body: settingsHTML})
}
