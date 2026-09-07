//go:build !js || !wasm

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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
	redirectURI = "http://127.0.0.1:56121/callback"
	authURL     = "https://auth.x.ai/oauth2/authorize"
)

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
	newPath := filepath.Join(home, ".config", "grok-oauth-proxy", "auth.json")

	// Migrate credentials from the pre-rename path so existing users aren't
	// logged out after upgrading. Only migrate when the new file is absent.
	if _, err := os.Stat(newPath); os.IsNotExist(err) {
		oldPath := filepath.Join(home, ".config", "grok-api-proxy", "auth.json")
		if _, err := os.Stat(oldPath); err == nil {
			if err := os.MkdirAll(filepath.Dir(newPath), 0700); err == nil {
				_ = os.Rename(oldPath, newPath)
			}
		}
	}

	return newPath
}

type fileTokenStore struct{}

func (fileTokenStore) SaveTokens(tokens AuthTokens) error {
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

func (fileTokenStore) LoadTokens() (*AuthTokens, error) {
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

func generateRandomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func generatePKCE() (verifier string, challenge string, err error) {
	verifier, err = generateRandomString(32)
	if err != nil {
		return "", "", err
	}
	h := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(h[:]), nil
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := generateRandomString(16)
	if err != nil {
		http.Error(w, "Failed to start OAuth login", http.StatusInternalServerError)
		return
	}
	nonce, err := generateRandomString(16)
	if err != nil {
		http.Error(w, "Failed to start OAuth login", http.StatusInternalServerError)
		return
	}
	verifier, challenge, err := generatePKCE()
	if err != nil {
		http.Error(w, "Failed to start OAuth login", http.StatusInternalServerError)
		return
	}

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

	tokens, err := requestTokens(r.Context(), url.Values{
		"grant_type":            {"authorization_code"},
		"code":                  {code},
		"redirect_uri":          {redirectURI},
		"client_id":             {clientID},
		"code_verifier":         {verifier},
		"code_challenge_method": {"S256"},
	}, "")
	if err != nil {
		http.Error(w, "Token exchange failed", http.StatusBadGateway)
		return
	}
	if err := saveTokens(*tokens); err != nil {
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

func handleProxy(p *httputil.ReverseProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If it's a request to /login or /callback, it shouldn't hit the proxy.
		// We handle those directly in main.

		tokens, ok := ensureAccessToken(w)
		if !ok {
			return
		}

		if isModelsListRequest(r) {
			handleModelsList(w, r, tokens)
			return
		}

		headers := make(http.Header)
		copyRequestHeaders(headers, r.Header)
		headers.Set("Authorization", "Bearer "+tokens.AccessToken)
		r.Header = headers

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
	configureRuntime(fileTokenStore{}, http.DefaultClient)

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
		query := req.URL.Query()
		query.Del("key")
		req.URL.RawQuery = query.Encode()

		// Normalize paths that start with /v1 so that tools which send
		// /v1/models or /v1/chat/completions still work correctly.
		if strings.HasPrefix(req.URL.Path, "/v1/") {
			req.URL.Path = "/" + strings.TrimPrefix(req.URL.Path, "/v1/")
		}

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

	// MCP endpoint. The handler is built once so the tool set is shared across
	// requests; the session itself is stateless.
	mcp := loggingMiddleware(adminMiddleware(mcpHandler()))
	mux.HandleFunc("/mcp", mcp)
	mux.HandleFunc("/mcp/", mcp)

	// /login and /callback are deliberately left ungated above: the OAuth flow
	// has to be reachable from the browser before a key can be used.
	mux.HandleFunc("/", loggingMiddleware(adminMiddleware(handleProxy(proxy))))

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
