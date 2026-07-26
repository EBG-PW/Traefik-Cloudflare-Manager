package main

import (
	"net/http"
	"testing"
	"time"
)

func TestHTTPServerHasDefensiveTimeouts(t *testing.T) {
	server := newHTTPServer(":0", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if server.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s", server.ReadHeaderTimeout)
	}
	if server.IdleTimeout != 90*time.Second {
		t.Fatalf("IdleTimeout = %s", server.IdleTimeout)
	}
	if server.MaxHeaderBytes != 1<<20 {
		t.Fatalf("MaxHeaderBytes = %d", server.MaxHeaderBytes)
	}
}
