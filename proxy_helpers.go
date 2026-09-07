package main

import (
	"net/http"
	"net/url"
	"strings"
)

func workerUpstreamURL(incoming *url.URL) (string, error) {
	path := incoming.Path
	if strings.HasPrefix(path, "/v1/") {
		path = "/" + strings.TrimPrefix(path, "/v1/")
	}
	path = strings.TrimPrefix(path, "/")
	query := incoming.Query()
	query.Del("key")
	upstream, err := url.Parse(apiURL + "/" + path)
	if err != nil {
		return "", err
	}
	upstream.RawQuery = query.Encode()
	return upstream.String(), nil
}

func copyRequestHeaders(destination, source http.Header) {
	for key, values := range source {
		if isProxyInternalHeader(key) {
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func isProxyInternalHeader(key string) bool {
	canonical := http.CanonicalHeaderKey(key)
	return isHopByHopHeader(canonical) || canonical == "Authorization" || canonical == "X-Api-Key" ||
		canonical == "Host" || canonical == "Content-Length" || canonical == "Accept-Encoding" ||
		canonical == "Forwarded" || canonical == "Cdn-Loop" || canonical == "X-Real-Ip" ||
		strings.HasPrefix(canonical, "Cf-") || strings.HasPrefix(canonical, "X-Forwarded-")
}

func copyResponseHeaders(destination, source http.Header) {
	for key, values := range source {
		if isHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func isHopByHopHeader(key string) bool {
	switch http.CanonicalHeaderKey(key) {
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}
