package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	kagi "github.com/hra42/kagi-go-sdk"
)

const defaultSnippetMax = 300

type bucket struct {
	name string
	kind string
	hits []kagi.SearchHit
}

func FormatSearch(result *kagi.SearchResult, opts formatOptions) (string, []SearchResultItem) {
	snippetMax := opts.snippetMax
	if snippetMax <= 0 {
		snippetMax = defaultSnippetMax
	}
	if result == nil {
		return noResultsHint(), nil
	}

	concise := opts.responseFormat == "concise"

	d := result.Data
	allBuckets := []bucket{
		{"Web", "web", d.Search},
		{"News", "news", d.News},
		{"Images", "image", d.Image},
		{"Videos", "video", d.Video},
		{"Podcasts", "podcast", d.Podcast},
		{"Direct Answer", "direct_answer", d.DirectAnswer},
		{"Infobox", "infobox", d.Infobox},
		{"Related Searches", "related_search", d.RelatedSearch},
	}

	var sb strings.Builder
	var items []SearchResultItem
	totalHits := 0

	for _, b := range allBuckets {
		if len(b.hits) == 0 {
			continue
		}
		if opts.fields != nil {
			if _, ok := opts.fields[b.kind]; !ok {
				continue
			}
		}
		hits := b.hits
		if concise && len(hits) > conciseResultsPerBkt {
			hits = hits[:conciseResultsPerBkt]
		}

		fmt.Fprintf(&sb, "## %s\n\n", b.name)
		for i, h := range hits {
			if concise {
				// Compact form: title + URL only. Saves context for the agent's follow-up extract.
				fmt.Fprintf(&sb, "%d. **%s** — %s\n", i+1, fallback(h.Title, "(untitled)"), h.URL)
			} else {
				fmt.Fprintf(&sb, "%d. **%s**\n   %s\n", i+1, fallback(h.Title, "(untitled)"), h.URL)
				if snippet := truncate(h.Snippet, snippetMax); snippet != "" {
					fmt.Fprintf(&sb, "   %s\n", snippet)
				}
				if h.Time != "" {
					fmt.Fprintf(&sb, "   _%s_\n", h.Time)
				}
			}
			sb.WriteString("\n")

			items = append(items, SearchResultItem{
				Type:    b.kind,
				Title:   h.Title,
				URL:     h.URL,
				Snippet: h.Snippet,
				Time:    h.Time,
			})
			totalHits++
		}
		if concise && len(b.hits) > conciseResultsPerBkt {
			fmt.Fprintf(&sb, "_…%d more %s results suppressed; re-run with response_format: \"detailed\" or paginate with page: 2 to see them._\n\n",
				len(b.hits)-conciseResultsPerBkt, b.name)
		}
	}

	if totalHits == 0 {
		return noResultsHint(), nil
	}

	// Trailing guidance: nudge the agent toward the obvious next step.
	if concise {
		sb.WriteString("---\n")
		sb.WriteString("Next: snippets are intentionally omitted in concise mode. To read full content, call `kagi_extract` with up to 10 of the URLs above. To re-rank or see snippets, re-run with `response_format: \"detailed\"`.\n")
	} else {
		sb.WriteString("---\n")
		sb.WriteString("Next: snippets are truncated and not authoritative. For full page content, call `kagi_extract` with up to 10 of the URLs above.\n")
	}

	out := strings.TrimRight(sb.String(), "\n")
	return capOutput(out, opts.maxOutputChars, "kagi_search"), items
}

func noResultsHint() string {
	return "No results.\n\nNext: broaden the query (drop quotes, site: filters, or rare terms), try a different `workflow` (news/images/etc.), or paginate with page: 2 if you were already deep."
}

// pagesIndexRE matches a Kagi ErrorDetail.Location of the form "pages[N].url" or "pages[N]".
var pagesIndexRE = regexp.MustCompile(`^pages\[(\d+)\]`)

func FormatExtract(result *kagi.ExtractResult, requestedURLs []string, opts formatOptions) (string, []ExtractItem) {
	if result == nil {
		return "No content extracted.\n\nNext: verify the URLs are reachable HTTPS pages, then retry. If a specific URL keeps failing, search for an archive.org mirror via kagi_search.", nil
	}

	errByURL := make(map[string]string, len(result.Errors))
	var orphanErrors []kagi.ErrorDetail
	for _, e := range result.Errors {
		if url := urlFromLocation(e.Location, requestedURLs); url != "" {
			errByURL[url] = formatErrorDetail(e) + recoveryHintFor(e.Code)
		} else {
			orphanErrors = append(orphanErrors, e)
		}
	}

	var sb strings.Builder
	items := make([]ExtractItem, 0, len(result.Data)+len(result.Errors))
	seen := make(map[string]struct{}, len(result.Data))
	totalErrors := 0
	totalPages := 0

	for _, page := range result.Data {
		fmt.Fprintf(&sb, "## %s\n\n", page.URL)
		md := page.Markdown
		if md == "" {
			sb.WriteString("_(no content)_")
		} else {
			truncated, cut := truncatePerURL(md, opts.perURLMaxChars)
			sb.WriteString(truncated)
			if cut > 0 {
				fmt.Fprintf(&sb, "\n\n_…page truncated; %d more characters available. Re-run kagi_extract on just this URL with a larger or zero max_chars to see the rest._", cut)
			}
		}
		sb.WriteString("\n\n")

		item := ExtractItem{URL: page.URL, Markdown: page.Markdown}
		if msg, ok := errByURL[page.URL]; ok {
			item.Error = msg
			totalErrors++
		}
		items = append(items, item)
		seen[page.URL] = struct{}{}
		totalPages++
	}

	// Requested URLs that errored without a corresponding Data entry.
	for url, msg := range errByURL {
		if _, ok := seen[url]; ok {
			continue
		}
		fmt.Fprintf(&sb, "## %s\n\n_error: %s_\n\n", url, msg)
		items = append(items, ExtractItem{URL: url, Error: msg})
		totalErrors++
	}

	if len(orphanErrors) > 0 {
		sb.WriteString("## Errors\n\n")
		for _, e := range orphanErrors {
			fmt.Fprintf(&sb, "- %s%s\n", formatErrorDetail(e), recoveryHintFor(e.Code))
			items = append(items, ExtractItem{Error: formatErrorDetail(e) + recoveryHintFor(e.Code)})
			totalErrors++
		}
	}

	if sb.Len() == 0 {
		return "No content extracted.\n\nNext: verify the URLs are reachable HTTPS pages, then retry.", items
	}

	if totalErrors > 0 && totalPages == 0 {
		sb.WriteString("\n---\nNext: every URL failed. Check the recovery hint on each error above; for `extract.timeout`, retry alone with a higher `timeout`; for `extract.invalid_url`, find a canonical URL via `kagi_search`.")
	}

	out := strings.TrimRight(sb.String(), "\n")
	return capOutput(out, opts.maxOutputChars, "kagi_extract"), items
}

func truncatePerURL(s string, n int) (string, int) {
	if n <= 0 || len(s) <= n {
		return s, 0
	}
	return s[:n], len(s) - n
}

// capOutput truncates the final formatted output to maxChars and appends a
// recovery hint when truncation happens. When maxChars <= 0 the default
// (defaultMaxOutputChars) is applied.
func capOutput(s string, maxChars int, tool string) string {
	if maxChars <= 0 {
		maxChars = defaultMaxOutputChars
	}
	if len(s) <= maxChars {
		return s
	}
	// Cut at a safe point and add a structured footer the agent can act on.
	cut := s[:maxChars]
	// Try to end on a newline for cleaner output.
	if idx := strings.LastIndex(cut, "\n"); idx > maxChars-200 && idx > 0 {
		cut = cut[:idx]
	}
	var hint string
	switch tool {
	case "kagi_search":
		hint = "Recover by (a) narrowing the query, (b) setting response_format: \"concise\", (c) restricting `fields`, or (d) calling kagi_extract on the URLs above to get full content one at a time."
	case "kagi_extract":
		hint = "Recover by (a) re-running on a smaller subset of URLs, (b) lowering `max_chars` per URL, or (c) extracting one URL at a time."
	default:
		hint = "Recover by reducing the requested scope."
	}
	return cut + fmt.Sprintf("\n\n---\n_Output truncated at %d characters to fit the agent's context window. %s_", maxChars, hint)
}

// recoveryHintFor maps Kagi error codes to short, actionable next-step text.
// Returns "" if no specific hint applies (the generic error message still
// surfaces). Kagi codes are namespaced like "extract.timeout".
func recoveryHintFor(code string) string {
	switch code {
	case "extract.timeout":
		return " — retry this URL alone with a higher `timeout` (up to 10s)."
	case "extract.invalid_url":
		return " — verify the URL or use `kagi_search` to find a canonical https URL."
	case "extract.unsupported_scheme":
		return " — only https:// is supported."
	case "extract.fetch_failed", "extract.unreachable":
		return " — the origin server failed or refused the request; search for an archive.org mirror, or skip."
	case "":
		return ""
	}
	return ""
}

func formatErrorDetail(e kagi.ErrorDetail) string {
	parts := []string{}
	if e.Code != "" {
		parts = append(parts, e.Code)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if len(parts) == 0 {
		return "unknown error"
	}
	return strings.Join(parts, ": ")
}

// urlFromLocation maps a Kagi ErrorDetail.Location (e.g. "pages[2].url") back
// to the originally-requested URL by index. Returns "" when the location does
// not match the expected shape or the index is out of range.
func urlFromLocation(loc string, requestedURLs []string) string {
	m := pagesIndexRE.FindStringSubmatch(loc)
	if m == nil {
		return ""
	}
	idx, err := strconv.Atoi(m[1])
	if err != nil || idx < 0 || idx >= len(requestedURLs) {
		return ""
	}
	return requestedURLs[idx]
}

func fallback(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
