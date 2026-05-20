# Grok API Proxy

A simple, local reverse-proxy built in Go that handles OAuth authentication against the xAI Grok API. It transparently adds your authorization credentials to API requests and manages token refreshes for you, letting you query the API without having to manually manage tokens or API keys.

## Features

- **Interactive OAuth Handshake:** Easily log in and get your OAuth tokens right from the terminal and your browser.
- **Transparent Proxying:** Send requests to `127.0.0.1:56121` just like you would to `api.x.ai/v1`, and the proxy takes care of forwarding them with the correct headers.
- **Automatic Token Refresh:** Access tokens are automatically refreshed in the background whenever they expire.
- **Secure Local Storage:** Credentials are saved locally to `~/.config/grok-api-proxy/auth.json`.

## Requirements

- Go 1.25.5+
- [mise](https://mise.jdx.dev/) for task running (optional, but recommended)

## Setup & Building

You can use `mise` to easily build and run the project, as tasks are defined in the included `mise.toml`.

### Building the binary
```bash
mise build
```
*(This will compile the binary to `./bin/proxy`)*

Alternatively, you can build it manually using standard Go tools:
```bash
go build -o ./bin/proxy .
```

## Usage

### 1. Authenticate

Before you can use the proxy, you need to perform an initial OAuth login to get your tokens.

Run the `auth` subcommand:
```bash
./bin/proxy auth
```
This will start a temporary server and automatically open your default browser. Complete the authorization flow in the browser. Once finished, you will see a success message and the tokens will be saved.

### 2. Run the Proxy

Start the persistent background proxy server:
```bash
mise run
```
*(Alternatively: `./bin/proxy`)*

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

## How It Works

1. The `auth` subcommand implements an OAuth 2.0 PKCE flow, saving the resulting `access_token` and `refresh_token`.
2. When the main proxy is running, any incoming request is checked against the saved tokens.
3. If the `access_token` is valid, it is injected into the headers as `Authorization: Bearer <token>`.
4. If it's expired (or close to expiring), the proxy uses the `refresh_token` to seamlessly fetch a new access token before forwarding the request.
5. Unauthenticated requests are immediately rejected with a `401 Unauthorized`.
