package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"log"
	"net/http"
	"strings"
)

// adminMiddleware checks the proxy key in the supported OpenAI-compatible locations.
func adminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminKey := getenv("ADMIN_API_KEY")
		if adminKey == "" {
			log.Print("ADMIN_API_KEY environment variable not set")
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "Admin API not configured", http.StatusInternalServerError)
			return
		}

		var providedToken string
		authHeader := r.Header.Get("Authorization")
		apiKeyHeader := r.Header.Get("X-API-Key")
		keyParam := r.URL.Query().Get("key")

		switch {
		case authHeader != "":
			parts := strings.Fields(authHeader)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				log.Printf("Invalid Authorization header format: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
				w.Header().Set("Cache-Control", "no-store")
				http.Error(w, "Invalid Authorization header format", http.StatusUnauthorized)
				return
			}
			providedToken = parts[1]
		case apiKeyHeader != "":
			providedToken = apiKeyHeader
		case keyParam != "":
			providedToken = keyParam
		default:
			log.Printf("Missing API key: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		providedHash := sha256.Sum256([]byte(providedToken))
		expectedHash := sha256.Sum256([]byte(adminKey))
		if subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) != 1 {
			log.Printf("Invalid API key: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}
