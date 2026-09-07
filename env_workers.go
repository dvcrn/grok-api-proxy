//go:build js && wasm

package main

import "github.com/syumai/workers-go/cloudflare"

func getenv(key string) string {
	return cloudflare.Getenv(key)
}
