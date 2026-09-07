//go:build !js || !wasm

package main

import "os"

func getenv(key string) string {
	return os.Getenv(key)
}
