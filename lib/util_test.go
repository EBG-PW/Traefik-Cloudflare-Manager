package lib

import "testing"

func TestManagerServiceURLUsesListenPort(t *testing.T) {
	tests := map[string]string{
		":7070":        "http://manager:7070",
		"0.0.0.0:7071": "http://manager:7071",
		"7072":         "http://manager:7072",
		"":             "http://manager:8080",
	}
	for listenAddr, expected := range tests {
		if got := ManagerServiceURL("manager", listenAddr); got != expected {
			t.Fatalf("ManagerServiceURL(%q) = %q, want %q", listenAddr, got, expected)
		}
	}
}
