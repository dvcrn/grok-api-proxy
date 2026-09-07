# Grok OAuth Proxy

Grok OAuth Proxy lets OpenAI-compatible clients use the xAI API through a SuperGrok subscription. It runs on your machine, handles the browser login and token refresh, and forwards requests to `api.x.ai` with the current OAuth token.

It also provides an MCP server at `/mcp`. Agents can use it to discover the Grok model IDs exposed by the proxy and send one-shot prompts without configuring an OpenAI API client.

Use it when a client can connect to an OpenAI-compatible base URL but cannot authenticate with Grok OAuth directly.

## Quick start

Install the proxy with npm:

```bash
npm install -g grok-oauth-proxy
```

Other installation options:

```bash
# mise
mise use -g go:github.com/dvcrn/grok-oauth-proxy@latest

# Go
go install github.com/dvcrn/grok-oauth-proxy@latest
```

Complete the browser login once:

```bash
grok-oauth-proxy auth
```

If the browser does not open, visit `http://127.0.0.1:56121/login`. The proxy saves credentials to `~/.config/grok-oauth-proxy/auth.json`.

Start the server with a key of your choice:

```bash
ADMIN_API_KEY="replace-with-a-long-random-value" grok-oauth-proxy
```

The proxy listens on `http://127.0.0.1:56121`. Send requests to its `/v1` base URL and use the admin key as the API key:

```bash
curl http://127.0.0.1:56121/v1/chat/completions \
  -H "Authorization: Bearer replace-with-your-admin-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-composer-2.5-fast",
    "messages": [{"role": "user", "content": "Say hello in one sentence."}],
    "stream": false
  }'
```

List the model IDs available to the signed-in account:

```bash
curl http://127.0.0.1:56121/v1/models \
  -H "Authorization: Bearer replace-with-your-admin-key"
```

## Client setup

Point OpenAI-compatible clients at `http://127.0.0.1:56121/v1`. For example, an OpenCode provider can use:

```json
{
  "provider": {
    "xai": {
      "options": {
        "baseURL": "http://127.0.0.1:56121/v1",
        "apiKey": "replace-with-your-admin-key"
      }
    }
  }
}
```

## Authentication

`grok-oauth-proxy auth` uses OAuth 2.0 with PKCE. The local callback stores the access token, refresh token, and expiry time in `~/.config/grok-oauth-proxy/auth.json`. While serving requests, the proxy refreshes the access token shortly before it expires.

`ADMIN_API_KEY` is separate from the Grok OAuth token. It controls access to the local proxy and MCP endpoint. Clients may send it as a bearer token, an `X-API-Key` header, or the `key` query parameter.

The local server binds to loopback only. Requests to `/login` and `/callback` remain available without the admin key so the browser flow can complete.

## Cloudflare Workers

Workers deployments store OAuth credentials in KV and send xAI traffic through a [Workers VPC](https://developers.cloudflare.com/workers-vpc/) tunnel. Create a tunnel in **Cloudflare Dashboard > Workers VPC > Tunnels**, run `cloudflared` on a machine with normal Internet access, and set its UUID on the `GROK_EGRESS` binding in `wrangler.toml`.

```bash
wrangler kv namespace create GROK_OAUTH_PROXY_KV
wrangler deploy
wrangler secret put ADMIN_API_KEY
```

Set the returned namespace ID on the `GROK_AUTH` binding. See Cloudflare's [tunnel setup](https://developers.cloudflare.com/workers-vpc/configuration/tunnel/) and [VPC Networks guide](https://developers.cloudflare.com/workers-vpc/configuration/vpc-networks/).

Start xAI device authorization with the protected admin API:

```bash
BASE_URL="https://grok-oauth-proxy.<SUBDOMAIN>.workers.dev"

curl -X POST "$BASE_URL/admin/auth/start" \
  -H "Authorization: Bearer $ADMIN_API_KEY"
```

Open the returned `verificationUrl`, enter `userCode` if prompted, then poll no faster than `retryAfterSeconds`:

```bash
curl -X POST "$BASE_URL/admin/auth/status" \
  -H "Authorization: Bearer $ADMIN_API_KEY"

curl "$BASE_URL/admin/status" \
  -H "Authorization: Bearer $ADMIN_API_KEY"
```

`POST /admin/tokens` supports manual setup with `accessToken`, `refreshToken`, and `expiresAt` in Unix milliseconds. Access tokens are refreshed five minutes before expiry and saved back to KV. All admin routes accept either the bearer header or `X-API-Key`.

## Endpoints

The proxy forwards xAI API paths, including:

| Endpoint | Purpose |
| --- | --- |
| `POST /v1/chat/completions` | OpenAI-compatible chat completions |
| `GET /v1/models` | Upstream models plus proxy-provided model entries |
| `POST /mcp` | Stateless MCP server with `ask_grok` and `ask_grok_models` |
| `GET /login` | Start the browser login |
| `GET /callback` | Complete the local OAuth callback |
| `POST /admin/auth/start` | Start Workers xAI device authorization |
| `GET /admin/auth/status` | Read the Workers authorization state |
| `POST /admin/auth/status` | Poll xAI and store approved tokens |
| `POST /admin/tokens` | Store OAuth tokens manually in Workers KV |
| `GET /admin/status` | Check whether Workers credentials are configured |
| `GET /health` | Workers health check |

## MCP clients

The `/mcp` endpoint uses stateless streamable HTTP with JSON responses. It keeps no conversation or session state between calls.

MCP requests use the proxy's Grok OAuth credentials upstream and the same `ADMIN_API_KEY` as the proxied API. No separate xAI API key is required.

MCP configuration varies by client. Configure a streamable HTTP server with:

| Setting | Value |
| --- | --- |
| URL | `http://127.0.0.1:56121/mcp` |
| Header | `Authorization: Bearer replace-with-your-admin-key` |

For clients that use an `mcpServers` JSON object:

```json
{
  "mcpServers": {
    "ask-grok": {
      "type": "http",
      "url": "http://127.0.0.1:56121/mcp",
      "headers": {
        "Authorization": "Bearer replace-with-your-admin-key"
      }
    }
  }
}
```

The client discovers these tools after it connects:

| Tool | Input | Result |
| --- | --- | --- |
| `ask_grok_models` | None | Upstream model IDs plus proxy-provided entries, with owner when present |
| `ask_grok` | `model`, `prompt` | The requested model, model that served the request, and response text |

Call `ask_grok_models` first when the model ID is not already known. Its results combine the xAI model list with the extra model entries supplied by the proxy.

`ask_grok` is one-shot. It does not retain conversation history, so `prompt` must include all context needed for that call. The returned `model` may differ from `requested_model` when xAI resolves an alias.

## Development

```bash
mise run test
mise run build
```
