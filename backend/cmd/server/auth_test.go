package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerAuthentication(t *testing.T) {
	for _, tc := range []struct {
		path, token string
		want        int
	}{
		{"/health", "", 204}, {"/ready", "", 401}, {"/api/v1/chat", "Bearer wrong", 401},
		{"/api/v1/chat", "Bearer secret", 204}, {"/api/models", "", 401},
	} {
		t.Run(tc.path+tc.token, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.path, nil)
			r.Header.Set("Authorization", tc.token)
			w := httptest.NewRecorder()
			bearerAuth("secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
		})
	}
}

func TestBrowserOriginsDenied(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/settings", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	corsMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("browser reached handler") })).ServeHTTP(w, r)
	if w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("browser access permitted: %d", w.Code)
	}
}
