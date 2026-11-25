package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

func TranslationCompatibilityMode() (string, error) {
	mode := strings.ToLower(strings.TrimSpace(GetOr("ROUTER_TRANSLATION_COMPATIBILITY_MODE", "shadow")))
	switch mode {
	case "off", "shadow", "enforce":
		return mode, nil
	default:
		return "", fmt.Errorf("invalid ROUTER_TRANSLATION_COMPATIBILITY_MODE %q (expected off, shadow, or enforce)", mode)
	}
}

func MustGet(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("Missing required env var: %s", key))
	}
	return v
}

func GetOr(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

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

func CodexExtraCreditsAllowed() (bool, error) {
	raw := strings.TrimSpace(GetOr("ROUTER_CODEX_ALLOW_EXTRA_CREDITS", "false"))
	switch strings.ToLower(raw) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("invalid ROUTER_CODEX_ALLOW_EXTRA_CREDITS %q (expected true or false)", raw)
	}
}

type ChannelBaseURLs struct {
	Stable string
	Beta   string
}

func ServingChannelBaseURLs() ChannelBaseURLs {
	return ChannelBaseURLs{
		Stable: strings.TrimSpace(GetOr("ROUTER_STABLE_BASE_URL", "")),
		Beta:   strings.TrimSpace(GetOr("ROUTER_BETA_BASE_URL", "")),
	}
}
