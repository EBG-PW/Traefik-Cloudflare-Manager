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

func TestValidUsername(t *testing.T) {
	valid := []string{"admin", "Admin-2", "first.last", "user_name"}
	for _, username := range valid {
		if !ValidUsername(username) {
			t.Errorf("ValidUsername(%q) rejected a valid username", username)
		}
	}
	invalid := []string{"", "-admin", ".admin", "user:name", "user name", "<img>", "ユーザー"}
	for _, username := range invalid {
		if ValidUsername(username) {
			t.Errorf("ValidUsername(%q) accepted an invalid username", username)
		}
	}
}
