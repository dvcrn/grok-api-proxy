# Grok API Proxy

A simple, local reverse-proxy that handles OAuth authentication against the xAI Grok API. It transparently adds your authorization credentials to API requests and manages token refreshes for you, letting you use your SuperGrok subscription to make requests against the xAI API. 



## Features

- **Interactive OAuth Handshake:** Easily log in and get your OAuth tokens right from the terminal and your browser.
- **Transparent Proxying:** Send requests to `127.0.0.1:56121` just like you would to `api.x.ai/v1`, and the proxy takes care of forwarding them with the correct headers.
- **Automatic Token Refresh:** Access tokens are automatically refreshed in the background whenever they expire.
- **Secure Local Storage:** Credentials are saved locally to `~/.config/grok-api-proxy/auth.json`.

## Requirements

- Go 1.25.5+
- [mise](https://mise.jdx.dev/) for task running (optional, but recommended)

## Installation

### With mise

```bash
mise use go:github.com/dvcrn/grok-api-proxy
```

### With Go

```bash
go install github.com/dvcrn/grok-api-proxy@latest
```

After installation, authenticate with:

```bash
grok-api-proxy auth
```

This writes your credentials to `~/.config/grok-api-proxy/auth.json`.

Then run the proxy:

```bash
grok-api-proxy
```

## Setup & Building (from source)

You can use `mise` to easily build and run the project, as tasks are defined in the included `mise.toml`.

### Building the binary

```bash
mise build
```

_(This will compile the binary to `./bin/proxy`)_

Alternatively, you can build it manually using standard Go tools:

```bash
go build -o ./bin/proxy .
```

## Usage

### 1. Authenticate

Before you can use the proxy, you need to perform an initial OAuth login to get your tokens.

Run the `auth` subcommand:

```bash
grok-api-proxy auth
```

This will start a temporary server and automatically open your default browser. Complete the authorization flow in the browser. Once finished, you will see a success message and the tokens will be saved.

(If you built from source: `./bin/proxy auth`)

### 2. Run the Proxy

Start the persistent background proxy server:

```bash
grok-api-proxy
```

(If you built from source: `mise run` or `./bin/proxy`)

The proxy server will now listen on `http://127.0.0.1:56121`.

### 3. Send Requests

You can now use `http://127.0.0.1:56121` as a drop-in replacement for the `https://api.x.ai/v1` base URL. You don't need to pass any Authorization headers; the proxy handles that for you.

**Example Chat Completion:**

```bash
curl http://127.0.0.1:56121/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [
      {
        "role": "user",
        "content": "Hello!"
      }
    ],
    "model": "grok-4.20-0309-reasoning",
    "stream": false
  }'
```

**Example Listing Models:**

```bash
curl http://127.0.0.1:56121/models
```

## Usage in Popular Tools

### OpenCode

Update the provider configuration in your opencode.json:

```json
  "provider": {
    "xai": {
      "options": {
        "baseURL": "http://localhost:56121",
        "apiKey": "x"
      }
    }
   }
```

### Zed

Add the following to your `~/.config/zed/settings.json` (or the project-local `.zed/settings.json`):

Check the latest models by running `curl http://localhost:56121/models`

```json
"language_models": {
  "x_ai": {
    "api_url": "http://localhost:56121",
    "available_models": [
        {
          "name": "grok-build-0.1",
          "display_name": "Grok Build 0.1",
          "max_tokens": 256000,
          "max_output_tokens": 32768,
          "max_completion_tokens": 32768,
          "parallel_tool_calls": true,
          "supports_images": true,
          "supports_tools": true,
        },
      {
        "name": "grok-4.3",
        "display_name": "Grok 4.3",
        "max_tokens": 131072,
        "max_output_tokens": 8192,
        "parallel_tool_calls": true,
        "supports_images": true,
        "supports_tools": true
      },
      {
        "name": "grok-4.20-0309-reasoning",
        "display_name": "Grok 4.20 Reasoning (0309)",
        "max_tokens": 131072,
        "max_output_tokens": 8192,
        "parallel_tool_calls": true,
        "supports_images": true,
        "supports_tools": true
      }
    ]
  }
}
```

## How It Works

1. The `auth` subcommand implements an OAuth 2.0 PKCE flow, saving the resulting `access_token` and `refresh_token`.
2. When the main proxy is running, any incoming request is checked against the saved tokens.
3. If the `access_token` is valid, it is injected into the headers as `Authorization: Bearer <token>`.
4. If it's expired (or close to expiring), the proxy uses the `refresh_token` to seamlessly fetch a new access token before forwarding the request.
5. Unauthenticated requests are immediately rejected with a `401 Unauthorized`.
