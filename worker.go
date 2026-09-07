//go:build js && wasm

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"syscall/js"

	workers "github.com/syumai/workers-go"
	"github.com/syumai/workers-go/cloudflare"
	"github.com/syumai/workers-go/cloudflare/fetch"
	"github.com/syumai/workers-go/cloudflare/kv"
)

const (
	workerCredentialsKey = "oauth-credentials"
	workerDeviceAuthKey  = "device-auth"
	workerRequestLimit   = 32 << 20
)

type workersHTTPClient struct {
	client *fetch.Client
}

func newWorkersHTTPClient() *workersHTTPClient {
	binding := cloudflare.GetBinding("GROK_EGRESS")
	namespace := js.Global().Get("Object").New()
	namespace.Set("fetch", binding.Get("fetch").Call("bind", binding))
	return &workersHTTPClient{
		client: fetch.NewClient(fetch.WithBinding(namespace)),
	}
}

func (c *workersHTTPClient) Do(req *http.Request) (*http.Response, error) {
	fetchReq, err := fetch.NewRequest(req.Context(), req.Method, req.URL.String(), req.Body)
	if err != nil {
		return nil, err
	}
	for key, values := range req.Header {
		for _, value := range values {
			fetchReq.Header.Set(key, value)
		}
	}
	return c.client.Do(fetchReq, &fetch.RequestInit{Redirect: fetch.RedirectModeManual})
}

type workersTokenStore struct {
	kv *kv.Namespace
}

func newWorkersTokenStore() (*workersTokenStore, error) {
	namespace, err := kv.NewNamespace("GROK_AUTH")
	if err != nil {
		return nil, fmt.Errorf("initialize GROK_AUTH: %w", err)
	}
	return &workersTokenStore{kv: namespace}, nil
}

func (s *workersTokenStore) LoadTokens() (*AuthTokens, error) {
	encoded, err := s.kv.GetString(workerCredentialsKey, nil)
	if err != nil {
		return nil, fmt.Errorf("load OAuth credentials: %w", err)
	}
	if encoded == "" || encoded == "<null>" {
		return nil, errors.New("no credentials found in KV")
	}
	var tokens AuthTokens
	if err := json.Unmarshal([]byte(encoded), &tokens); err != nil {
		return nil, fmt.Errorf("decode OAuth credentials: %w", err)
	}
	return &tokens, nil
}

func (s *workersTokenStore) SaveTokens(tokens AuthTokens) error {
	encoded, err := json.Marshal(tokens)
	if err != nil {
		return fmt.Errorf("encode OAuth credentials: %w", err)
	}
	if err := s.kv.PutString(workerCredentialsKey, string(encoded), nil); err != nil {
		return fmt.Errorf("save OAuth credentials: %w", err)
	}
	return nil
}

func (s *workersTokenStore) LoadDeviceAuthSession() ([]byte, error) {
	encoded, err := s.kv.GetString(workerDeviceAuthKey, nil)
	if err != nil {
		return nil, fmt.Errorf("load device auth session: %w", err)
	}
	if encoded == "" || encoded == "<null>" {
		return nil, nil
	}
	return []byte(encoded), nil
}

func (s *workersTokenStore) SaveDeviceAuthSession(session []byte) error {
	if err := s.kv.PutString(workerDeviceAuthKey, string(session), nil); err != nil {
		return fmt.Errorf("save device auth session: %w", err)
	}
	return nil
}

func (s *workersTokenStore) CompleteDeviceAuth(tokens AuthTokens) error {
	if err := s.SaveTokens(tokens); err != nil {
		return err
	}
	return s.SaveDeviceAuthSession([]byte(`{"status":"authenticated"}`))
}

func main() {
	store, err := newWorkersTokenStore()
	if err != nil {
		log.Fatalf("initialize credentials: %v", err)
	}
	client := newWorkersHTTPClient()
	configureRuntime(store, client)
	auth := newDeviceAuth(store, client)

	mux := http.NewServeMux()
	registerAdminRoutes(mux, auth)
	mcp := adminMiddleware(mcpHandler())
	mux.HandleFunc("/mcp", mcp)
	mux.HandleFunc("/mcp/", mcp)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeAdminJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/", adminMiddleware(workerProxyHandler))
	workers.Serve(mux)
}

func workerProxyHandler(w http.ResponseWriter, r *http.Request) {
	tokens, ok := ensureAccessToken(w)
	if !ok {
		return
	}
	if isModelsListRequest(r) {
		handleModelsList(w, r, tokens)
		return
	}

	body, ok := readLimitedRequestBody(w, r, workerRequestLimit)
	if !ok {
		return
	}
	upstream, err := workerUpstreamURL(r.URL)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, errorResponse{Error: "Invalid upstream request"})
		return
	}
	resp, err := sendWorkerUpstreamRequest(r, upstream, body, tokens.AccessToken)
	if err != nil {
		log.Printf("upstream request failed: %v", err)
		writeJSONError(w, http.StatusBadGateway, errorResponse{Error: "Upstream request failed"})
		return
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		tokens, err = forceRefreshToken(tokens.AccessToken)
		if err != nil {
			log.Printf("refresh after upstream rejection failed: %v", err)
			writeJSONError(w, http.StatusUnauthorized, tokenExpiredResponse)
			return
		}
		resp, err = sendWorkerUpstreamRequest(r, upstream, body, tokens.AccessToken)
		if err != nil {
			log.Printf("upstream retry failed: %v", err)
			writeJSONError(w, http.StatusBadGateway, errorResponse{Error: "Upstream request failed"})
			return
		}
	}
	defer resp.Body.Close()
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("copy upstream response: %v", err)
	}
}

func sendWorkerUpstreamRequest(incoming *http.Request, upstream string, body []byte, accessToken string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, upstream, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyRequestHeaders(req.Header, incoming.Header)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	return runtimeClient.Do(req)
}
