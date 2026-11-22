package main

import (
	"net/http"
	"time"
)

// httpClientWithTimeout is a shared HTTP client with sensible timeouts for
// backend API calls and file downloads. It replaces bare http.DefaultClient
// usage which has no timeout and can hang goroutines indefinitely when a
// server stalls.
//
//isoform:destination chat_bridges,metadata
var httpClientWithTimeout = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 15 * time.Second
	transport.MaxIdleConns = 20
	transport.IdleConnTimeout = 90 * time.Second

	return &http.Client{
		Timeout:   60 * time.Second,
		Transport: transport,
	}
}()
