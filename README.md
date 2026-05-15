# kagi-search-mcp

A local [MCP](https://modelcontextprotocol.io) server that exposes the [Kagi](https://kagi.com) API as tools, built in Go on top of:

- [`github.com/hra42/kagi-go-sdk`](https://github.com/hra42/kagi-go-sdk)
- [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk)

Transports: **stdio** (default) and **Streamable HTTP** (via `--http`).

## Requirements

- Go 1.26+
- A Kagi API key — get one at https://kagi.com/settings?p=api

## Build

```bash
go mod tidy
go build -o kagi-search-mcp .
```

## Configure in Claude Desktop

Edit `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS):

```json
{
  "mcpServers": {
    "kagi": {
      "command": "/absolute/path/to/kagi-search-mcp",
      "env": { "KAGI_API_KEY": "sk-..." }
    }
  }
}
```

Restart Claude Desktop. The `kagi_search` and `kagi_extract` tools should appear.

## HTTP transport

Run the server as an HTTP service with Streamable HTTP transport:

```bash
KAGI_API_KEY=sk-... \
MCP_AUTH_TOKEN=$(openssl rand -hex 32) \
./kagi-search-mcp --http 127.0.0.1:8080
```

Flags:

- `--http <addr>` — listen address (e.g. `:8080`, `127.0.0.1:8080`). Omit to use stdio.
- `--http-path <path>` — URL path for the MCP endpoint (default `/mcp`).

Required env:

- `KAGI_API_KEY` — Kagi API key (same as stdio mode).
- `MCP_AUTH_TOKEN` — shared bearer token. Clients must send `Authorization: Bearer <token>`. Requests without it get `401`.

Quick check:

```bash
curl -X POST http://127.0.0.1:8080/mcp \
  -H "Authorization: Bearer $MCP_AUTH_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

**Security notes:** there's no TLS — bind to `127.0.0.1` for local use, or front the server with a reverse proxy (Caddy, nginx) that terminates TLS. The bearer token is a shared secret; rotate it like any other credential.

## Tools

### `kagi_search`

Searches the web with Kagi.

| Field | Type | Notes |
|---|---|---|
| `query` | string (required) | Search query |
| `limit` | int | 1–1024, capped server-side |
| `page` | int | 1–10 |
| `safe_search` | `"on"` \| `"off"` | Omit to inherit account default |
| `workflow` | `search` \| `images` \| `videos` \| `news` \| `podcasts` | Defaults to `search` |

Returns formatted markdown text plus a structured `results` array with `type`, `title`, `url`, `snippet`, `time`.

### `kagi_extract`

Fetches page content as markdown.

| Field | Type | Notes |
|---|---|---|
| `urls` | string[] (required) | 1–10 HTTPS URLs |
| `timeout` | number | Overall timeout in seconds (0.5–10) |

Returns concatenated markdown plus a structured `items` array.

## Troubleshooting

- **Server exits immediately with "KAGI_API_KEY environment variable is required"** — set the env var in the MCP client config.
- **401 Unauthorized** — invalid or revoked key.
- **429 Rate limited** — the SDK retries with backoff; persistent 429s mean the account quota is exhausted.

## License

Unlicense (public domain).
