package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	clientID     = "b1a00492-073a-47ea-816f-4c329264a828"
	redirectURI  = "http://127.0.0.1:56121/callback"
	scope        = "openid profile email offline_access grok-cli:access api:access"
	authURL      = "https://auth.x.ai/oauth2/authorize"
	tokenURL     = "https://auth.x.ai/oauth2/token"
	apiURL       = "https://api.x.ai/v1"
)

type AuthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // Unix timestamp in seconds
}

var (
	// In-memory store for PKCE verifiers keyed by state
	stateVerifiers = make(map[string]string)
	mu             sync.Mutex

	isAuthMode   bool
	authComplete = make(chan bool)
)

func getAuthFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Failed to get home dir: %v", err)
	}
	return filepath.Join(home, ".config", "grok-api-proxy", "auth.json")
}

func saveTokens(tokens AuthTokens) error {
	path := getAuthFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func loadTokens() (*AuthTokens, error) {
	path := getAuthFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tokens AuthTokens
	if err := json.Unmarshal(data, &tokens); err != nil {
		return nil, err
	}
	return &tokens, nil
}

func generateRandomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func generatePKCE() (verifier string, challenge string) {
	verifier = generateRandomString(32)
	h := sha256.New()
	h.Write([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	return
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	state := generateRandomString(16)
	nonce := generateRandomString(16)
	verifier, challenge := generatePKCE()

	mu.Lock()
	stateVerifiers[state] = verifier
	mu.Unlock()

	u, _ := url.Parse(authURL)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", scope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("plan", "generic")
	q.Set("referrer", "hermes-agent")
	u.RawQuery = q.Encode()

	http.Redirect(w, r, u.String(), http.StatusTemporaryRedirect)
}

func handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	errDesc := r.URL.Query().Get("error_description")

	if errDesc != "" {
		http.Error(w, "OAuth Error: "+errDesc, http.StatusBadRequest)
		return
	}
	if code == "" || state == "" {
		http.Error(w, "Missing code or state", http.StatusBadRequest)
		return
	}

	mu.Lock()
	verifier, exists := stateVerifiers[state]
	if exists {
		delete(stateVerifiers, state)
	}
	mu.Unlock()

	if !exists {
		http.Error(w, "Invalid state parameter", http.StatusBadRequest)
		return
	}

	// Exchange code for tokens
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	data.Set("client_id", clientID)
	data.Set("code_verifier", verifier)
	data.Set("code_challenge_method", "S256") // Just in case, the TS implementation sends it though usually it's only in /authorize

	resp, err := http.PostForm(tokenURL, data)
	if err != nil {
		http.Error(w, "Token exchange failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		http.Error(w, fmt.Sprintf("Token exchange failed (%d): %s", resp.StatusCode, string(body)), http.StatusInternalServerError)
		return
	}

	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		http.Error(w, "Failed to parse token response", http.StatusInternalServerError)
		return
	}

	tokens := AuthTokens{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Unix() + int64(tr.ExpiresIn),
	}
	if err := saveTokens(tokens); err != nil {
		http.Error(w, "Failed to save tokens: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<html><body><h1>Login successful!</h1><p>You can now close this tab and use the proxy.</p></body></html>`))

	if isAuthMode {
		go func() {
			time.Sleep(1 * time.Second) // give the response time to be sent
			authComplete <- true
		}()
	}
}

func refreshToken(refreshToken string) (*AuthTokens, error) {
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("client_id", clientID)
	data.Set("refresh_token", refreshToken)

	resp, err := http.PostForm(tokenURL, data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, string(body))
	}

	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, err
	}

	// Sometimes refresh token is not returned, keep the old one
	newRefresh := tr.RefreshToken
	if newRefresh == "" {
		newRefresh = refreshToken
	}

	tokens := AuthTokens{
		AccessToken:  tr.AccessToken,
		RefreshToken: newRefresh,
		ExpiresAt:    time.Now().Unix() + int64(tr.ExpiresIn),
	}
	if err := saveTokens(tokens); err != nil {
		return nil, err
	}

	return &tokens, nil
}

func handleProxy(p *httputil.ReverseProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If it's a request to /login or /callback, it shouldn't hit the proxy.
		// We handle those directly in main.
		
		tokens, err := loadTokens()
		if err != nil || tokens.AccessToken == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error": "Unauthorized", "message": "Please visit http://127.0.0.1:56121/login in your browser to authenticate."}`))
			return
		}

		// Check expiry with 2 minutes skew
		if time.Now().Unix() > tokens.ExpiresAt-120 {
			newTokens, err := refreshToken(tokens.RefreshToken)
			if err != nil {
				log.Printf("Failed to refresh token: %v", err)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error": "Token expired", "message": "Failed to refresh token. Please visit http://127.0.0.1:56121/login again."}`))
				return
			}
			tokens = newTokens
		}

		// Inject Authorization header
		r.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
		
		// Serve the request via proxy
		p.ServeHTTP(w, r)
	}
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func loggingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{w, http.StatusOK}
		
		next(rw, r)
		
		duration := time.Since(start)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rw.statusCode, duration)
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "auth" {
		isAuthMode = true
	}

	target, _ := url.Parse(apiURL)
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Update the request to match the target host
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
		
		// Ensure the path is properly formatted:
		// Target path is /v1. If request path is /chat/completions, 
		// we want /v1/chat/completions
		if !strings.HasPrefix(req.URL.Path, target.Path) {
			req.URL.Path = strings.TrimSuffix(target.Path, "/") + "/" + strings.TrimPrefix(req.URL.Path, "/")
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/login", loggingMiddleware(handleLogin))
	mux.HandleFunc("/callback", loggingMiddleware(handleCallback))
	mux.HandleFunc("/", loggingMiddleware(handleProxy(proxy)))

	server := &http.Server{
		Addr:    "127.0.0.1:56121",
		Handler: mux,
	}

	if isAuthMode {
		go func() {
			log.Println("Starting temporary auth server on http://127.0.0.1:56121")
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Server failed: %v", err)
			}
		}()

		log.Println("Opening browser to authenticate...")
		time.Sleep(500 * time.Millisecond) // Wait a bit for server to start
		err := exec.Command("open", "http://127.0.0.1:56121/login").Start()
		if err != nil {
			log.Printf("Failed to open browser, please navigate to http://127.0.0.1:56121/login manually")
		}

		<-authComplete
		log.Println("Authentication successful! Tokens saved.")
		os.Exit(0)
	} else {
		log.Println("Starting Grok API proxy on http://127.0.0.1:56121")
		if err := server.ListenAndServe(); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}
}
