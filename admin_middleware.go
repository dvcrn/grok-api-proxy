package main

import (
	"log"
	"net/http"
	"os"
	"strings"
)

// adminMiddleware checks for a valid admin API key from either
// 'Authorization: Bearer <key>' or 'X-API-Key: <key>' headers, or the 'key'
// query parameter. The key comes from ADMIN_API_KEY.
func adminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminKey := os.Getenv("ADMIN_API_KEY")
		if adminKey == "" {
			log.Print("ADMIN_API_KEY environment variable not set")
			http.Error(w, "Admin API not configured", http.StatusInternalServerError)
			return
		}

		var providedToken string
		authHeader := r.Header.Get("Authorization")
		apiKeyHeader := r.Header.Get("X-API-Key")
		keyParam := r.URL.Query().Get("key")

		switch {
		case authHeader != "":
			// Expect "Bearer <token>" format, case-insensitive
			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				log.Printf("Invalid Authorization header format for admin endpoint: %s %s from %s",
					r.Method, r.RequestURI, r.RemoteAddr)
				http.Error(w, "Invalid Authorization header format", http.StatusUnauthorized)
				return
			}
			providedToken = parts[1]
		case apiKeyHeader != "":
			providedToken = apiKeyHeader
		case keyParam != "":
			providedToken = keyParam
		default:
			log.Printf("Missing required Authorization header, X-API-Key header, or key query parameter for admin endpoint: %s %s from %s",
				r.Method, r.RequestURI, r.RemoteAddr)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		if providedToken != adminKey {
			log.Printf("Invalid admin API key provided: %s %s from %s",
				r.Method, r.RequestURI, r.RemoteAddr)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}
