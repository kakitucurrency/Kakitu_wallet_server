package net

import (
	"net/http"
	"time"
)

// A separate interface for HTTPClient allows us to create a mock implementation

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

var (
	Client HTTPClient
)

func init() {
	Client = &http.Client{
		Timeout: 30 * time.Second,
	}
}
