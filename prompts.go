package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerPrompts wires the Kagi MCP prompts onto server. Prompts are
// user-discoverable templates surfaced in MCP clients (e.g. the slash-prompt
// menu in Claude Desktop / Claude Code). Each prompt expands to a structured
// instruction telling the LLM which of the existing tools to call and how to
// format the answer — no Kagi API calls happen inside these handlers.
func registerPrompts(server *mcp.Server) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "research",
		Title:       "Research a topic",
		Description: "Deep-dive: search + extract authoritative sources + cited brief.",
		Arguments: []*mcp.PromptArgument{
			{Name: "topic", Description: "Topic to research", Required: true},
			{Name: "depth", Description: "shallow | deep (default deep)"},
		},
	}, researchHandler)

	server.AddPrompt(&mcp.Prompt{
		Name:        "fact-check",
		Title:       "Fact-check a claim",
		Description: "Verify a claim against diverse, independent sources and return a verdict with evidence.",
		Arguments: []*mcp.PromptArgument{
			{Name: "claim", Description: "The claim to verify", Required: true},
		},
	}, factCheckHandler)

	server.AddPrompt(&mcp.Prompt{
		Name:        "compare-sources",
		Title:       "Compare perspectives on a topic",
		Description: "Surface multiple distinct viewpoints on a topic with per-perspective evidence and a neutral synthesis.",
		Arguments: []*mcp.PromptArgument{
			{Name: "topic", Description: "Topic to compare perspectives on", Required: true},
			{Name: "perspectives", Description: "Number of distinct viewpoints (2-5, default 3)"},
		},
	}, compareSourcesHandler)

	server.AddPrompt(&mcp.Prompt{
		Name:        "find-primary-sources",
		Title:       "Find primary sources",
		Description: "Surface authoritative originals (papers, filings, official statements) rather than commentary.",
		Arguments: []*mcp.PromptArgument{
			{Name: "topic", Description: "Topic to find primary sources for", Required: true},
		},
	}, findPrimarySourcesHandler)

	server.AddPrompt(&mcp.Prompt{
		Name:        "summarize-url",
		Title:       "Summarize a URL",
		Description: "Fetch a single HTTPS URL and produce a structured summary.",
		Arguments: []*mcp.PromptArgument{
			{Name: "url", Description: "HTTPS URL to summarize", Required: true},
			{Name: "focus", Description: "Optional aspect to emphasize"},
		},
	}, summarizeURLHandler)
}

// --- helpers ---

func requireArg(req *mcp.GetPromptRequest, key string) (string, error) {
	if req == nil || req.Params == nil {
		return "", fmt.Errorf("argument %q is required", key)
	}
	v := strings.TrimSpace(req.Params.Arguments[key])
	if v == "" {
		return "", fmt.Errorf("argument %q is required", key)
	}
	return v, nil
}

func optArg(req *mcp.GetPromptRequest, key, def string) string {
	if req == nil || req.Params == nil {
		return def
	}
	v := strings.TrimSpace(req.Params.Arguments[key])
	if v == "" {
		return def
	}
	return v
}

func parseClampInt(s string, lo, hi, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func textResult(description, text string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{
		Description: description,
		Messages: []*mcp.PromptMessage{
			{Role: "user", Content: &mcp.TextContent{Text: text}},
		},
	}
}

// --- templates ---

const researchTemplate = `Research the topic **%q** using the Kagi tools available to you.

Step 1. Call ` + "`kagi_search`" + ` with query: %q, response_format: "concise", limit: 10. If depth is "deep", also issue 1–2 follow-up searches that target sub-aspects you find in the first result set.

Step 2. From the combined results, pick the %d most authoritative URLs (prefer primary sources, official docs, peer-reviewed work, and reputable outlets over aggregators).

Step 3. Call ` + "`kagi_extract`" + ` once with those URLs and max_chars: 6000.

Step 4. Synthesize a structured brief with: (a) one-paragraph TL;DR, (b) key findings as bullets with inline source numbers, (c) a numbered "Sources" list with title + URL, (d) any open questions or contested claims.

Cite every non-trivial claim with [n] referencing the Sources list. If sources disagree, note it explicitly.

(Depth: %s)`

const factCheckTemplate = `Fact-check the following claim using Kagi:

> %s

Step 1. Run ` + "`kagi_search`" + ` with a neutral, paraphrased version of the claim (avoid leading wording), response_format: "concise", limit: 10. Then run one search for the strongest counter-position you can construct.

Step 2. From both result sets, choose 3–5 URLs spanning multiple independent publishers/domains. Prefer primary sources (court filings, papers, official statements) over secondary reporting.

Step 3. ` + "`kagi_extract`" + ` those URLs with max_chars: 5000.

Step 4. Render a verdict block:
**Verdict:** one of ` + "`True` | `Mostly true` | `Mixed` | `Mostly false` | `False` | `Unverifiable`" + `.
Follow with 3–6 bullets of supporting evidence (with [n] cites), 1–3 bullets of contradicting evidence, a note on source independence/quality, and your confidence level.

Do not declare a verdict if sources conflict and you cannot resolve the conflict — return ` + "`Unverifiable`" + ` with the reason.`

const compareSourcesTemplate = `Build a multi-perspective view of **%q** using Kagi, surfacing %d distinct viewpoints.

Step 1. Run ` + "`kagi_search`" + ` for the topic (response_format: "concise", limit: 10). Scan domains and snippets to identify at least %d distinct viewpoints (e.g., proponent / critic / neutral analyst; or differing political, regional, or academic stances).

Step 2. Select one URL per perspective and call ` + "`kagi_extract`" + ` (max_chars: 5000) on the batch.

Step 3. For each perspective, output:
**{Perspective label}** — Source: title + URL.
Then 3–5 bullets summarizing the position in its own framing (not yours). Then one bullet on what this perspective omits or downplays.

Step 4. End with a short neutral synthesis: where do the perspectives agree, and what is the actual axis of disagreement? Cite throughout.`

const findPrimarySourcesTemplate = `Find the primary/authoritative sources for **%q** — not commentary about it.

Step 1. Run ` + "`kagi_search`" + ` with operators that bias toward originals. Try in this order, stopping when you have enough quality hits:
- ` + "`%q site:gov OR site:edu OR site:org`" + `
- ` + "`%q filetype:pdf`" + ` (often surfaces papers, filings, standards)
- ` + "`%q \"official\" OR \"press release\" OR \"announcement\"`" + `
Use response_format: "concise", limit: 10 for each.

Step 2. From the combined results, select up to 5 URLs that are genuinely primary: official statements, original research, court documents, standards bodies, dataset publishers, the author/creator's own page. Reject aggregators, listicles, and SEO blogspam.

Step 3. Call ` + "`kagi_extract`" + ` on the selected URLs (max_chars: 4000) only if the user is likely to want a summary; otherwise just return the list with a one-line provenance note per source.

Output: a numbered list. Each entry = **Title** — URL — one-line note on what type of primary source it is (paper / filing / press release / dataset / etc.) and the publisher.`

const summarizeURLTemplateNoFocus = `Summarize the page at %s.

Step 1. Call ` + "`kagi_extract`" + ` with urls: [%q], max_chars: 0 (read the full page).

Step 2. Produce: (a) a 2–3 sentence TL;DR, (b) a structured outline of the page's main sections/arguments, (c) key facts or numbers as bullets, (d) any caveats — author bias, date staleness, missing context.

Step 3. If the extract returned an error for the URL, do not fabricate a summary — report the error verbatim and suggest the user check the URL or try an https mirror.`

const summarizeURLTemplateWithFocus = `Summarize the page at %s, focusing on **%s**.

Step 1. Call ` + "`kagi_extract`" + ` with urls: [%q], max_chars: 0 (read the full page).

Step 2. Produce: (a) a 2–3 sentence TL;DR weighted toward the focus, (b) a structured outline of the page's main sections/arguments, (c) key facts or numbers as bullets — emphasize material relevant to the focus, (d) any caveats — author bias, date staleness, missing context.

Step 3. If the extract returned an error for the URL, do not fabricate a summary — report the error verbatim and suggest the user check the URL or try an https mirror.`

// --- handlers ---

func researchHandler(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	topic, err := requireArg(req, "topic")
	if err != nil {
		return nil, err
	}
	depth := optArg(req, "depth", "deep")
	if depth != "shallow" && depth != "deep" {
		depth = "deep"
	}
	pickN := 5
	if depth == "shallow" {
		pickN = 3
	}
	text := fmt.Sprintf(researchTemplate, topic, topic, pickN, depth)
	return textResult("Research brief on "+topic, text), nil
}

func factCheckHandler(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	claim, err := requireArg(req, "claim")
	if err != nil {
		return nil, err
	}
	text := fmt.Sprintf(factCheckTemplate, claim)
	return textResult("Fact-check: "+claim, text), nil
}

func compareSourcesHandler(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	topic, err := requireArg(req, "topic")
	if err != nil {
		return nil, err
	}
	persp := parseClampInt(optArg(req, "perspectives", ""), 2, 5, 3)
	text := fmt.Sprintf(compareSourcesTemplate, topic, persp, persp)
	return textResult("Perspectives on "+topic, text), nil
}

func findPrimarySourcesHandler(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	topic, err := requireArg(req, "topic")
	if err != nil {
		return nil, err
	}
	text := fmt.Sprintf(findPrimarySourcesTemplate, topic, topic, topic, topic)
	return textResult("Primary sources for "+topic, text), nil
}

func summarizeURLHandler(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	raw, err := requireArg(req, "url")
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("argument %q is not a valid URL: %w", "url", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("argument %q must use https (got %q)", "url", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("argument %q must include a host", "url")
	}
	focus := optArg(req, "focus", "")
	var text string
	if focus == "" {
		text = fmt.Sprintf(summarizeURLTemplateNoFocus, raw, raw)
	} else {
		text = fmt.Sprintf(summarizeURLTemplateWithFocus, raw, focus, raw)
	}
	return textResult("Summary of "+raw, text), nil
}
