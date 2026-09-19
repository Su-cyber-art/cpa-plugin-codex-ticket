package ticket

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Host uses only read-only host.auth callbacks. No auth.save or scheduler API.
type Host interface {
	List(context.Context) ([]pluginapi.HostAuthFileEntry, error)
	Get(context.Context, string) (pluginapi.HostAuthGetResponse, error)
	Runtime(context.Context, string) (pluginapi.HostAuthFileEntry, error)
}
type credential struct{ token, account, identity string }

func eligible(a pluginapi.HostAuthFileEntry) bool {
	p := a.Provider
	if p == "" {
		p = a.Type
	}
	return p == "codex" && a.AuthIndex != "" && len(a.AuthIndex) <= 128 && a.Status == "active" && !a.Disabled && !a.Unavailable && !a.RuntimeOnly && a.Source == "file" && a.Path != "" && a.Size <= maxCredentialBytes && !isAPIKey(a.AccountType)
}
func isAPIKey(s string) bool {
	s = strings.ToLower(strings.ReplaceAll(s, "-", "_"))
	return s == "api_key" || s == "apikey"
}
func text(m map[string]any, k string) string { s, _ := m[k].(string); return strings.TrimSpace(s) }
func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(parts[1]) > 65536 {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}
func parseCredential(raw []byte, a pluginapi.HostAuthFileEntry) (credential, error) {
	bad := errors.New("credential_unknown")
	if len(raw) == 0 || len(raw) > maxCredentialBytes || !eligible(a) {
		return credential{}, bad
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return credential{}, bad
	}
	if p := text(m, "type"); p != "" && p != "codex" {
		return credential{}, bad
	}
	if disabled, _ := m["disabled"].(bool); disabled {
		return credential{}, bad
	}
	for _, k := range []string{"api_key", "api-key", "OPENAI_API_KEY"} {
		if text(m, k) != "" {
			return credential{}, errors.New("api_key_skipped")
		}
	}
	if isAPIKey(text(m, "auth_mode")) || isAPIKey(text(m, "account_type")) {
		return credential{}, errors.New("api_key_skipped")
	}
	t := m
	if nested, ok := m["tokens"].(map[string]any); ok {
		t = nested
	}
	token := text(t, "access_token")
	if token == "" {
		token = text(m, "access_token")
	}
	if token == "" || len(token) > 65536 || strings.ContainsAny(token, "\r\n\x00 \t") {
		return credential{}, bad
	}
	// Conflicting account identifiers are not guessed. ID JWT is a fallback from
	// an already trusted host file, not an authentication verifier.
	var ids []string
	for _, obj := range []map[string]any{m, t} {
		for _, key := range []string{"account_id", "chatgpt_account_id"} {
			if id := text(obj, key); id != "" {
				ids = append(ids, id)
			}
		}
	}
	idtoken := text(t, "id_token")
	if idtoken == "" {
		idtoken = text(m, "id_token")
	}
	claims := jwtClaims(idtoken)
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	if id := text(auth, "chatgpt_account_id"); id != "" {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return credential{}, bad
	}
	account := ids[0]
	if len(account) > 512 || strings.ContainsAny(account, "\r\n\x00 \t") {
		return credential{}, bad
	}
	for _, id := range ids {
		if id != account {
			return credential{}, errors.New("identity_conflict")
		}
	}
	// Stable over access/refresh token rotation, different for another account or
	// subject rebound onto the same auth index. Never expose this fingerprint.
	sum := sha256.Sum256([]byte(account + "\x00" + text(claims, "sub") + "\x00" + a.ID + "\x00" + a.Path))
	return credential{token: token, account: account, identity: hex.EncodeToString(sum[:])}, nil
}
