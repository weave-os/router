package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// TranslationCompatibilityMode parses ROUTER_TRANSLATION_COMPATIBILITY_MODE.
// Callers must reject an invalid value at startup; the error must not silently
// change request behavior at runtime.
func TranslationCompatibilityMode() (string, error) {
	mode := strings.ToLower(strings.TrimSpace(GetOr("ROUTER_TRANSLATION_COMPATIBILITY_MODE", "shadow")))
	switch mode {
	case "off", "shadow", "enforce":
		return mode, nil
	default:
		return "", fmt.Errorf("invalid ROUTER_TRANSLATION_COMPATIBILITY_MODE %q (expected off, shadow, or enforce)", mode)
	}
}

// MustGet returns the env var value for key, panicking if it is unset or empty.
func MustGet(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("Missing required env var: %s", key))
	}
	return v
}

// GetOr returns the env var value for key, or defaultValue if it is unset or empty.
func GetOr(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// ParseModelIDMap parses ROUTER_MODEL_ID_MAP: comma-separated catalog=upstream
// pairs. Empty/unset returns nil (today's no-rewrite behavior). Invalid pairs
// fail closed so a typo cannot silently skip a mapping.
func ParseModelIDMap(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	out := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			return nil, fmt.Errorf("invalid ROUTER_MODEL_ID_MAP pair %q (expected catalog=upstream)", pair)
		}
		from, to, ok := strings.Cut(pair, "=")
		from = strings.TrimSpace(from)
		to = strings.TrimSpace(to)
		if !ok || from == "" || to == "" {
			return nil, fmt.Errorf("invalid ROUTER_MODEL_ID_MAP pair %q (expected catalog=upstream)", pair)
		}
		if _, dup := out[from]; dup {
			return nil, fmt.Errorf("duplicate ROUTER_MODEL_ID_MAP catalog id %q", from)
		}
		out[from] = to
	}
	return out, nil
}

// PostgresDSN returns DATABASE_URL when set, otherwise composes one from POSTGRES_* env vars.
//
// On Cloud Run with Cloud SQL, POSTGRES_CONNECTION_NAME routes through the Auth Proxy Unix
// socket at /cloudsql/<connection-name>, which handles TLS+IAM upstream. Omitting it falls
// through to TCP+sslmode; an instance that requires a trusted client certificate on that
// path is served by the POSTGRES_CLIENT_CERT material in internal/postgres/pgtls.
func PostgresDSN() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	user := MustGet("POSTGRES_USER")
	password := MustGet("POSTGRES_PASSWORD")
	db := MustGet("POSTGRES_DB")

	if conn := os.Getenv("POSTGRES_CONNECTION_NAME"); conn != "" {
		return fmt.Sprintf(
			"postgres://%s:%s@/%s?host=/cloudsql/%s",
			url.QueryEscape(user),
			url.QueryEscape(password),
			url.PathEscape(db),
			conn,
		)
	}

	host := MustGet("POSTGRES_HOST")
	port := GetOr("POSTGRES_PORT", "5432")
	// Default to require so managed Postgres works out of the box; local Docker must set POSTGRES_SSLMODE=disable.
	sslMode := GetOr("POSTGRES_SSLMODE", "require")
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=%s",
		url.QueryEscape(user),
		url.QueryEscape(password),
		host,
		port,
		url.PathEscape(db),
		sslMode,
	)
}
