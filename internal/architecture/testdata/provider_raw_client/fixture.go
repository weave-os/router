package providerrawclient

import (
	"net/http"
	"time"
)

var client = &http.Client{Transport: http.DefaultTransport, Timeout: 30 * time.Second}

func fetch(url string) (*http.Response, error) {
	if response, err := client.Get(url); err == nil {
		return response, nil
	}
	return http.Get(url)
}
