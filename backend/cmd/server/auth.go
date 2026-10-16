package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
)

// Hash both values to fixed lengths before comparing. No credentials are logged.
func bearerAuth(token string) func(http.Handler) http.Handler {
	expected := sha256.Sum256([]byte("Bearer " + token))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" && token != "" {
				actual := sha256.Sum256([]byte(r.Header.Get("Authorization")))
				if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
					w.Header().Set("WWW-Authenticate", "Bearer")
					apiResponse(w, http.StatusUnauthorized, nil, "unauthorized")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
