package trafficcapture

import (
	"errors"
	"net/url"
	"strings"
)

type credentialQueryKey string

const (
	queryAPIKey          credentialQueryKey = "api_key"
	queryAPIKeyCompact   credentialQueryKey = "apikey"
	queryAccessToken     credentialQueryKey = "access_token"
	queryAuthToken       credentialQueryKey = "auth_token"
	queryAuthorization   credentialQueryKey = "authorization"
	queryClientSecret    credentialQueryKey = "client_secret"
	queryCredential      credentialQueryKey = "credential"
	queryCredentials     credentialQueryKey = "credentials"
	queryIDToken         credentialQueryKey = "id_token"
	queryKey             credentialQueryKey = "key"
	queryPassword        credentialQueryKey = "password"
	querySecret          credentialQueryKey = "secret"
	querySignature       credentialQueryKey = "signature"
	querySig             credentialQueryKey = "sig"
	queryToken           credentialQueryKey = "token"
	queryRefreshToken    credentialQueryKey = "refresh_token"
	queryXAPIKey         credentialQueryKey = "x_api_key"
	queryXGoogAPIKey     credentialQueryKey = "x_goog_api_key"
	queryXAMZCredential  credentialQueryKey = "x_amz_credential"
	queryXAMZSignature   credentialQueryKey = "x_amz_signature"
	queryXGoogCredential credentialQueryKey = "x_goog_credential"
	queryXGoogSignature  credentialQueryKey = "x_goog_signature"
)

// RedactURL hides userinfo and common credential query parameters in captured URLs.
func RedactURL(rawURL string) string {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "[REDACTED URL]"
	}
	if parsedURL.User != nil {
		parsedURL.User = url.User("[REDACTED]")
	}
	if parsedURL.RawQuery != "" {
		queryParts := strings.Split(parsedURL.RawQuery, "&")
		for index, queryPart := range queryParts {
			rawKey, _, _ := strings.Cut(queryPart, "=")
			key, err := url.QueryUnescape(rawKey)
			if err != nil {
				parsedURL.RawQuery = "[REDACTED]"
				break
			}
			if isCredentialQueryKey(key) {
				queryParts[index] = rawKey + "=%5BREDACTED%5D"
			}
		}
		if parsedURL.RawQuery != "[REDACTED]" {
			parsedURL.RawQuery = strings.Join(queryParts, "&")
		}
	}
	return parsedURL.String()
}

// RedactRequestError prevents net/http URL errors from persisting query credentials.
func RedactRequestError(err error) string {
	var requestURLError *url.Error
	if errors.As(err, &requestURLError) {
		return (&url.Error{
			Op:  requestURLError.Op,
			URL: RedactURL(requestURLError.URL),
			Err: requestURLError.Err,
		}).Error()
	}
	return err.Error()
}

func isCredentialQueryKey(key string) bool {
	switch credentialQueryKey(strings.ToLower(strings.ReplaceAll(key, "-", "_"))) {
	case queryAPIKey, queryAPIKeyCompact, queryAccessToken,
		queryAuthToken, queryAuthorization, queryClientSecret,
		queryCredential, queryCredentials, queryIDToken,
		queryKey, queryPassword, querySecret, querySignature,
		querySig, queryToken, queryRefreshToken, queryXAPIKey,
		queryXGoogAPIKey, queryXAMZCredential, queryXAMZSignature,
		queryXGoogCredential, queryXGoogSignature:
		return true
	default:
		return false
	}
}
