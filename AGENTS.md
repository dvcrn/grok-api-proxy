- Repo: dvcrn/grok-oauth-proxy

# Development instructions

- Use `mise run format`, `mise run test`, and `mise run build` after Go changes.
- Use `mise run build-worker` for the Cloudflare Workers WASM build.
- Keep OAuth tokens and `ADMIN_API_KEY` out of source, logs, and commits.
- Use the repository's npm lockfile and npm for changes under `npm/`.
- Preserve the local proxy's loopback-only browser login while keeping Workers authentication routes behind the admin middleware.
- Workers provider and OAuth traffic must use the `GROK_EGRESS` VPC binding.
