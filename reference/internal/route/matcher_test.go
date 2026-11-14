package route

import (
	"net/http/httptest"
	"testing"
)

func TestBestUsesLongestPrefix(t *testing.T) {
	rules := []Rule{
		{Match: Match{PathPrefix: "/api/"}, Value: "api"},
		{Match: Match{PathPrefix: "/api/users/"}, Value: "users"},
		{Match: Match{PathPrefix: "/api/orders/"}, Value: "orders"},
	}

	request := httptest.NewRequest("GET", "http://gateway.local/api/users/1", nil)

	index, ok := Best(rules, request)
	if !ok {
		t.Fatal("expected a match")
	}

	if rules[index].Value != "users" {
		t.Fatalf("got %v, want users", rules[index].Value)
	}
}

func TestMatchMethodsAndHeaders(t *testing.T) {
	match := Match{
		PathPrefix: "/api/",
		Methods:    []string{"GET", "HEAD"},
		Headers:    map[string]string{"X-Canary": "true"},
	}

	request := httptest.NewRequest("GET", "http://gateway.local/api/users/1", nil)
	request.Header.Set("X-Canary", "true")

	if !match.Matches(request) {
		t.Fatal("expected match")
	}

	request.Method = "POST"
	if match.Matches(request) {
		t.Fatal("POST should not match")
	}

	request.Method = "GET"
	request.Header.Set("X-Canary", "false")
	if match.Matches(request) {
		t.Fatal("wrong header value should not match")
	}
}

func TestHostMatchIgnoresPort(t *testing.T) {
	match := Match{Host: "api.example.com", PathPrefix: "/"}

	request := httptest.NewRequest("GET", "http://api.example.com:8080/health", nil)
	if !match.Matches(request) {
		t.Fatal("expected host match without port")
	}
}
