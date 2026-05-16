package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	kagi "github.com/hra42/kagi-go-sdk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// kagiClient is the subset of *kagi.Client used by the MCP tools. Defining
// the interface here lets tests inject a fake without depending on the SDK's
// concrete HTTP client.
type kagiClient interface {
	Search(ctx context.Context, req kagi.SearchRequest) (*kagi.SearchResult, error)
	Extract(ctx context.Context, req kagi.ExtractRequest) (*kagi.ExtractResult, error)
}

const searchToolDescription = `Search the web with Kagi. Returns ranked results grouped by category.

Workflow: this is the primary entry point. Snippets are short (truncated, default 300 chars); they are not authoritative content. To read a result's full content, follow up with kagi_extract on the URL.

Inputs:
- query (required): the search query. Kagi supports operators like site:, intitle:, intext:, plus quoted phrases.
- limit (1-100, default Kagi's choice): results per page.
- page (1-10): for pagination through the same query.
- safe_search ("on"|"off"): omit to inherit the account default.
- workflow: which Kagi index to query.
  * "search"   — general web (default)
  * "news"     — recent news articles
  * "images"   — image results
  * "videos"   — video results
  * "podcasts" — podcast episodes
- response_format ("concise"|"detailed", default "concise"):
  * "concise"  — top 5 results per bucket, titles + URLs only, no snippets. Use this when the goal is to discover URLs to extract.
  * "detailed" — full ranked results with snippets. Use when the snippets themselves are enough to answer.
- fields: optional list of result categories to keep. Defaults to all. Allowed: web, news, image, video, podcast, direct_answer, infobox, related_search.

The output is a markdown string plus a structured 'results' array. The markdown is what gets injected into your context — prefer "concise" + a follow-up extract for read-heavy tasks.`

const extractToolDescription = `Extract page content as markdown from one or more HTTPS URLs (1-10 per call). Read-only.

Workflow: typically called after kagi_search to read the full content of selected URLs. Do not call this for URLs you have not seen — the parameter is "URLs", not "queries".

Inputs:
- urls (required, 1-10): HTTPS URLs to extract. http:// is rejected; find an https mirror or skip.
- timeout (0.5-10 seconds): overall budget across all URLs.
- max_chars (>= 0, default 0): truncate each page's markdown to this many characters. Use a smaller value (e.g. 4000) when scanning many pages; use 0 (no truncation) when reading one page deeply.

The output is a markdown string plus a structured 'items' array. Each item carries either 'markdown' or an 'error' for that URL. Per-URL failures don't fail the whole call.`

type SearchInput struct {
	Query          string   `json:"query" jsonschema:"the search query (required, non-empty)"`
	Limit          int      `json:"limit,omitempty" jsonschema:"results per page (1-100)"`
	Page           int      `json:"page,omitempty" jsonschema:"page number (1-10)"`
	SafeSearch     string   `json:"safe_search,omitempty" jsonschema:"'on' or 'off'; omit to inherit server default"`
	Workflow       string   `json:"workflow,omitempty" jsonschema:"one of: search, images, videos, news, podcasts"`
	ResponseFormat string   `json:"response_format,omitempty" jsonschema:"'concise' (default; top-5, urls only) or 'detailed' (full snippets)"`
	Fields         []string `json:"fields,omitempty" jsonschema:"optional list of buckets to keep: web, news, image, video, podcast, direct_answer, infobox, related_search"`
}

type SearchResultItem struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	Time    string `json:"time,omitempty"`
}

type SearchOutput struct {
	Text    string             `json:"text"`
	Results []SearchResultItem `json:"results"`
}

type ExtractInput struct {
	URLs     []string `json:"urls" jsonschema:"URLs to extract content from (1-10, HTTPS only)"`
	Timeout  float64  `json:"timeout,omitempty" jsonschema:"overall timeout in seconds (0.5-10)"`
	MaxChars int      `json:"max_chars,omitempty" jsonschema:"per-URL markdown truncation in characters; 0 = no truncation"`
}

type ExtractItem struct {
	URL      string `json:"url"`
	Markdown string `json:"markdown,omitempty"`
	Error    string `json:"error,omitempty"`
}

type ExtractOutput struct {
	Text  string        `json:"text"`
	Items []ExtractItem `json:"items"`
}

// Allowed values for input validation. Kept in sync with the Kagi SDK's
// Workflow constants so we fail fast at the MCP boundary rather than burning
// a request on a server-side rejection.
var (
	allowedWorkflows = map[string]struct{}{
		string(kagi.WorkflowSearch):   {},
		string(kagi.WorkflowImages):   {},
		string(kagi.WorkflowVideos):   {},
		string(kagi.WorkflowNews):     {},
		string(kagi.WorkflowPodcasts): {},
	}

	allowedFields = map[string]struct{}{
		"web": {}, "news": {}, "image": {}, "video": {}, "podcast": {},
		"direct_answer": {}, "infobox": {}, "related_search": {},
	}

	allowedResponseFormats = map[string]struct{}{
		"concise":  {},
		"detailed": {},
	}
)

const (
	maxSearchLimit         = 100
	maxSearchPage          = 10
	maxExtractURLs         = 10
	minTimeoutSec          = 0.5
	maxTimeoutSec          = 10
	defaultResponseFormat  = "concise"
	conciseResultsPerBkt   = 5
	defaultMaxOutputChars  = 100_000 // ~25k tokens; matches Anthropic's Claude Code default cap
)

// formatOptions threads tool-level options into the formatters.
type formatOptions struct {
	snippetMax     int
	maxOutputChars int
	responseFormat string
	fields         map[string]struct{} // nil = keep all
	perURLMaxChars int                 // for extract; 0 = unlimited
}

// registerTools wires the Kagi MCP tools onto server. snippetMax controls
// per-result snippet truncation; maxOutputChars caps the formatted markdown
// output of either tool to prevent context blowout.
func registerTools(server *mcp.Server, client kagiClient, snippetMax, maxOutputChars int) {
	readOnly := true
	notDestructive := false
	openWorld := true

	mcp.AddTool(server, &mcp.Tool{
		Name:        "kagi_search",
		Title:       "Kagi Web Search",
		Description: searchToolDescription,
		Annotations: &mcp.ToolAnnotations{
			Title:           "Kagi Web Search",
			ReadOnlyHint:    readOnly,
			DestructiveHint: &notDestructive,
			OpenWorldHint:   &openWorld,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		if msg := validateSearchInput(in); msg != "" {
			return toolError(msg), SearchOutput{}, nil
		}

		req := kagi.SearchRequest{Query: strings.TrimSpace(in.Query)}
		if in.Limit > 0 {
			req.Limit = in.Limit
		}
		if in.Page > 0 {
			req.Page = in.Page
		}
		if in.Workflow != "" {
			req.Workflow = kagi.Workflow(in.Workflow)
		}
		switch in.SafeSearch {
		case "on":
			b := true
			req.SafeSearch = &b
		case "off":
			b := false
			req.SafeSearch = &b
		}

		result, err := client.Search(ctx, req)
		if err != nil {
			if apiErr := classifyAPIError(err); apiErr != "" {
				return toolError(apiErr), SearchOutput{}, nil
			}
			return nil, SearchOutput{}, err
		}

		opts := formatOptions{
			snippetMax:     snippetMax,
			maxOutputChars: maxOutputChars,
			responseFormat: resolveResponseFormat(in.ResponseFormat),
			fields:         fieldSet(in.Fields),
		}
		text, items := FormatSearch(result, opts)
		out := SearchOutput{Text: text, Results: items}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "kagi_extract",
		Title:       "Kagi URL Extract",
		Description: extractToolDescription,
		Annotations: &mcp.ToolAnnotations{
			Title:           "Kagi URL Extract",
			ReadOnlyHint:    readOnly,
			DestructiveHint: &notDestructive,
			OpenWorldHint:   &openWorld,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ExtractInput) (*mcp.CallToolResult, ExtractOutput, error) {
		if msg := validateExtractInput(in); msg != "" {
			return toolError(msg), ExtractOutput{}, nil
		}

		pages := make([]kagi.ExtractPage, len(in.URLs))
		for i, u := range in.URLs {
			pages[i] = kagi.ExtractPage{URL: u}
		}
		req := kagi.ExtractRequest{Pages: pages}
		if in.Timeout > 0 {
			req.Timeout = in.Timeout
		}

		result, err := client.Extract(ctx, req)
		if err != nil {
			if apiErr := classifyAPIError(err); apiErr != "" {
				return toolError(apiErr), ExtractOutput{}, nil
			}
			return nil, ExtractOutput{}, err
		}

		opts := formatOptions{
			maxOutputChars: maxOutputChars,
			perURLMaxChars: in.MaxChars,
		}
		text, items := FormatExtract(result, in.URLs, opts)
		out := ExtractOutput{Text: text, Items: items}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, out, nil
	})
}

func validateSearchInput(in SearchInput) string {
	if strings.TrimSpace(in.Query) == "" {
		return "query must be a non-empty string"
	}
	if in.Limit < 0 || in.Limit > maxSearchLimit {
		return fmt.Sprintf("limit must be between 1 and %d", maxSearchLimit)
	}
	if in.Page < 0 || in.Page > maxSearchPage {
		return fmt.Sprintf("page must be between 1 and %d", maxSearchPage)
	}
	if in.SafeSearch != "" && in.SafeSearch != "on" && in.SafeSearch != "off" {
		return "safe_search must be 'on' or 'off'"
	}
	if in.Workflow != "" {
		if _, ok := allowedWorkflows[in.Workflow]; !ok {
			return "workflow must be one of: search, images, videos, news, podcasts"
		}
	}
	if in.ResponseFormat != "" {
		if _, ok := allowedResponseFormats[in.ResponseFormat]; !ok {
			return "response_format must be 'concise' or 'detailed'"
		}
	}
	for _, f := range in.Fields {
		if _, ok := allowedFields[f]; !ok {
			return fmt.Sprintf("fields: %q is not one of web, news, image, video, podcast, direct_answer, infobox, related_search", f)
		}
	}
	return ""
}

func validateExtractInput(in ExtractInput) string {
	if len(in.URLs) == 0 {
		return "urls must contain at least one URL"
	}
	if len(in.URLs) > maxExtractURLs {
		return fmt.Sprintf("urls may contain at most %d entries", maxExtractURLs)
	}
	for i, raw := range in.URLs {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Sprintf("urls[%d] must be a valid https:// URL (got %q)", i, raw)
		}
	}
	if in.Timeout != 0 && (in.Timeout < minTimeoutSec || in.Timeout > maxTimeoutSec) {
		return fmt.Sprintf("timeout must be between %.1f and %d seconds", minTimeoutSec, maxTimeoutSec)
	}
	if in.MaxChars < 0 {
		return "max_chars must be >= 0 (0 = no truncation)"
	}
	return ""
}

func resolveResponseFormat(v string) string {
	if v == "" {
		return defaultResponseFormat
	}
	return v
}

func fieldSet(fields []string) map[string]struct{} {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		out[f] = struct{}{}
	}
	return out
}

// classifyAPIError returns a user-facing message with a recovery hint for
// Kagi API errors the model should see. Returns "" for transport-level
// errors so the caller can propagate them as Go errors and let the MCP
// framework apply its retry policy.
func classifyAPIError(err error) string {
	var apiErr *kagi.APIError
	if !errors.As(err, &apiErr) {
		return ""
	}
	switch {
	case errors.Is(err, kagi.ErrUnauthorized):
		return "kagi api: unauthorized — the KAGI_API_KEY on the server is missing or invalid. Ask the operator to verify the key at https://kagi.com/settings?p=api; do not retry."
	case errors.Is(err, kagi.ErrRateLimited):
		if apiErr.RetryAfter > 0 {
			return fmt.Sprintf("kagi api: rate limited — retry after %s. If this persists the account quota is exhausted; reduce call frequency or upgrade the Kagi plan.", apiErr.RetryAfter)
		}
		return "kagi api: rate limited — back off briefly and retry. If this persists the account quota is exhausted; reduce call frequency."
	case errors.Is(err, kagi.ErrBadRequest):
		// Surface Kagi's specific error code + a generic recovery cue.
		return apiErr.Error() + " — check the input against the tool's documented constraints; for an unknown lens_id or unsupported workflow, omit the parameter and retry."
	case errors.Is(err, kagi.ErrServerError):
		return fmt.Sprintf("kagi api: server error (%d). Treated as transient; retry once with backoff, then surface the failure to the user.", apiErr.StatusCode)
	}
	return ""
}

func toolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}
