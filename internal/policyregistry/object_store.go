package policyregistry

import (
	"errors"
	"fmt"
	"strings"
)

const MaxPolicyObjectBytes = 8 << 20

var (
	ErrNotFound = errors.New("router policy registry object not found")
	ErrConflict = errors.New("router policy lane-head generation conflict")
)

func ParseGCSObjectURI(uri string) (string, string, error) {
	if !strings.HasPrefix(uri, "gs://") {
		return "", "", fmt.Errorf("invalid GCS URI %q", uri)
	}
	remainder := strings.TrimPrefix(uri, "gs://")
	bucketName, name, found := strings.Cut(remainder, "/")
	if bucketName == "" || !found || strings.Trim(name, "/") == "" {
		return "", "", fmt.Errorf("invalid GCS object URI %q", uri)
	}
	if strings.Contains(name, "//") || strings.Contains(name, "..") {
		return "", "", fmt.Errorf("invalid GCS object path %q", name)
	}
	return bucketName, strings.Trim(name, "/"), nil
}
