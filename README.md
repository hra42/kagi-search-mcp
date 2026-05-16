# kagi-search-mcp

A local [MCP](https://modelcontextprotocol.io) server that exposes the [Kagi](https://kagi.com) API as tools, built in Go on top of:

- [`github.com/hra42/kagi-go-sdk`](https://github.com/hra42/kagi-go-sdk)
- [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk)

Transports: **stdio** (default) and **Streamable HTTP** (via `--http`).

## Install (Claude Desktop, one-click)

1. Download the latest `.mcpb` for your platform from the [Releases page](https://github.com/hra42/kagi-search-mcp/releases/latest):
   - macOS Apple Silicon: `kagi-search-mcp-vX.Y.Z-darwin-arm64.mcpb`
   - macOS Intel:         `kagi-search-mcp-vX.Y.Z-darwin-amd64.mcpb`
   - Linux x86_64:        `kagi-search-mcp-vX.Y.Z-linux-amd64.mcpb`
   - Windows x86_64:      `kagi-search-mcp-vX.Y.Z-windows-amd64.mcpb`
2. Double-click the file. Claude Desktop shows an install dialog.
3. Paste your Kagi API key (get one at https://kagi.com/settings?p=api) when prompted.
4. Done — the search tools and prompts appear in the next conversation.

> **Note:** Claude Desktop will show an "unverified publisher" warning on install. This is normal — the bundle is not code-signed. Verify the download came from this repo's official releases page.

For other MCP clients (Claude Code, VS Code Copilot, custom integrations), see [Manual install](#manual-install) below.

## Requirements

- Go 1.26+
- A Kagi API key — get one at https://kagi.com/settings?p=api

## Build

```bash
go mod tidy
go build -o kagi-search-mcp .
```

## Manual install

For clients that don't support MCPB bundles, or to run the server from source: edit the client's MCP server config. For Claude Desktop, that's `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS):

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

Optional env:

- `LOG_LEVEL` — `debug` \| `info` (default) \| `warn` \| `error`. Logs are emitted as structured JSON on stderr.
- `MCP_RATE_RPS` — per-remote-IP refill rate for the token-bucket rate limiter (default `5`).
- `MCP_RATE_BURST` — per-remote-IP burst capacity (default `20`). Excess requests get `429` with `Retry-After: 1`.
- `KAGI_SNIPPET_MAX` — max snippet length in the formatted **detailed** search markdown (default `300`).
- `KAGI_MAX_OUTPUT_CHARS` — global cap on the formatted markdown returned by either tool (default `100000` ≈ 25k tokens). When exceeded, the output is truncated and an actionable footer is appended telling the agent how to recover.

Built-in endpoints:

- `GET /healthz` — always `200 OK` (no auth). For liveness probes.
- `GET /readyz` — `200` when the server has its API key configured, `503` otherwise. For readiness probes.
- `GET /<http-path>` and `POST /<http-path>` — MCP Streamable HTTP, bearer-auth required.

Every authenticated request carries an `X-Request-ID` header (echoed from the client or generated server-side) and is logged with method, path, status, duration, and remote IP. Query strings and the `Authorization` header are deliberately **not** logged.

Quick check:

```bash
curl -X POST http://127.0.0.1:8080/mcp \
  -H "Authorization: Bearer $MCP_AUTH_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

### Security model

- **Transport.** There's no TLS — bind to `127.0.0.1` for local use, or front the server with a reverse proxy (Caddy, nginx) that terminates TLS.
- **Auth.** A single shared bearer token (`MCP_AUTH_TOKEN`) gates the MCP endpoint. This is appropriate for trusted single-tenant deployments. For multi-tenant or production use, terminate **OAuth 2.1** at a gateway in front of this server (the [MCP spec](https://modelcontextprotocol.io) standardizes OAuth 2.1 for HTTP transports). Rotate the token like any other credential.
- **Rate limiting.** A per-IP token bucket protects the upstream Kagi API and the local process; tune via `MCP_RATE_RPS` / `MCP_RATE_BURST`.
- **Audit logging.** All requests are logged as structured JSON. PII-adjacent fields (`Authorization`, query string, request body) are intentionally omitted; if your environment requires fuller audit trails, capture them at the reverse proxy layer.
- **Tool annotations.** Both tools advertise `readOnlyHint: true` and `destructiveHint: false` so MCP clients can apply least-privilege policies.

## Design notes

The server is built around the recommendations from Anthropic's [Writing tools for agents](https://www.anthropic.com/engineering/writing-tools-for-agents) and [Code execution with MCP](https://www.anthropic.com/engineering/code-execution-with-mcp) — most importantly that **output tokens are the scarce resource** and tool responses should help the agent pick the next action.

- **Two tools, not ten.** `kagi_search` returns ranked URLs; `kagi_extract` reads their contents. No 1:1 wrappers around every Kagi endpoint.
- **`response_format: "concise"` is the default.** Concise mode returns the top 5 hits per bucket with titles + URLs only — no snippets. This is the right default when the agent's next step is to extract one of those URLs anyway. Pass `response_format: "detailed"` for the full snippet view.
- **Field filtering.** `fields: ["web", "news"]` restricts the response to those buckets only.
- **Per-URL truncation on extract.** `max_chars: 4000` caps each page's markdown when scanning many pages. The structured `items[].markdown` preserves the full content so downstream code (or a follow-up call) can read more without re-fetching.
- **Global output cap.** Every response is truncated to `KAGI_MAX_OUTPUT_CHARS` characters (default 100k ≈ 25k tokens, matching Claude Code's default). When truncated, the footer tells the agent how to recover (narrow the query, use concise mode, extract URLs one at a time, etc.).
- **Next-step hints.** Every response ends with a short prose hint pointing at the natural follow-up tool call. No silent dead ends.
- **Server `instructions`.** The Initialize response includes a short workflow primer that MCP clients (Claude Code, Claude Desktop, VS Code Copilot) inject into the system prompt at session start.
- **Errors are actionable.** Kagi 401/429/400/5xx are surfaced as `IsError: true` with a recovery hint (retry-after duration, "verify the key", "omit the parameter", etc.). Per-URL extract failures are correlated back to the requested URL and tagged with code-specific hints (`extract.timeout` → "retry alone with a higher timeout").

## Tools

### `kagi_search`

| Field | Type | Default | Notes |
|---|---|---|---|
| `query` | string (required) | — | Supports Kagi operators (`site:`, `intitle:`, quoted phrases) |
| `limit` | int 1–100 | Kagi's choice | Results per page |
| `page` | int 1–10 | 1 | Pagination |
| `safe_search` | `"on"` \| `"off"` | account default | |
| `workflow` | `search` \| `images` \| `videos` \| `news` \| `podcasts` | `search` | |
| `response_format` | `"concise"` \| `"detailed"` | `"concise"` | Concise = titles+URLs only, top 5 per bucket |
| `fields` | string[] | all | Buckets to keep: `web`, `news`, `image`, `video`, `podcast`, `direct_answer`, `infobox`, `related_search` |

Returns markdown text + a structured `results` array with `type`, `title`, `url`, `snippet`, `time`. The structured array always includes the full snippet even in concise mode.

### `kagi_extract`

| Field | Type | Default | Notes |
|---|---|---|---|
| `urls` | string[] (required) | — | 1–10 HTTPS URLs |
| `timeout` | number | server default | Overall timeout in seconds (0.5–10) |
| `max_chars` | int ≥ 0 | 0 = unlimited | Per-URL markdown truncation in the formatted text |

Returns concatenated markdown + a structured `items` array. Each item carries either `markdown` (full, never truncated in the structured field) or an `error`. Per-URL failures don't fail the call.

## Prompts

The server also exposes MCP prompts — pre-built templates that compose `kagi_search` and `kagi_extract` into common research workflows. In compatible clients (Claude Desktop, Claude Code, VS Code Copilot) they appear in the slash-prompt / prompt-picker menu. Each prompt returns a single user message that instructs the LLM which tools to call and how to format the answer.

| Prompt | Arguments | What it does |
|---|---|---|
| `research` | `topic` (required), `depth` (`shallow` \| `deep`, default `deep`) | Search → pick 3–5 authoritative URLs → extract → cited brief with TL;DR, findings, sources, open questions |
| `fact-check` | `claim` (required) | Neutral search + counter-position search → 3–5 independent sources → extract → verdict block + supporting/contradicting evidence + confidence |
| `compare-sources` | `topic` (required), `perspectives` (int 2–5, default 3) | Surface N distinct viewpoints, one source each, then a neutral synthesis of the actual axis of disagreement |
| `find-primary-sources` | `topic` (required) | Serial searches biased toward originals (`site:gov/edu/org`, `filetype:pdf`, official statements); returns a curated list with provenance notes |
| `summarize-url` | `url` (required, https), `focus` (optional) | Extract a single page in full → TL;DR + outline + key facts + caveats, optionally weighted toward a focus |

Prompts make no Kagi API calls themselves — they only emit text that guides the LLM to use the existing tools.

## Development

```bash
go test -race ./...
go vet ./...
golangci-lint run   # optional, mirrors CI
```

CI runs `vet`, `test -race`, `golangci-lint`, and `build` on every push and PR (see `.github/workflows/ci.yml`).

## Troubleshooting

- **Server exits immediately with "KAGI_API_KEY environment variable is required"** — set the env var in the MCP client config.
- **401 Unauthorized** — invalid or revoked key.
- **429 Rate limited** — the SDK retries with backoff; persistent 429s mean the account quota is exhausted.

## License

Unlicense (public domain).
