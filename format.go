package main

import (
	"fmt"
	"strings"

	kagi "github.com/hra42/kagi-go-sdk"
)

const snippetMax = 300

type bucket struct {
	name string
	kind string
	hits []kagi.SearchHit
}

func FormatSearch(result *kagi.SearchResult) (string, []SearchResultItem) {
	if result == nil {
		return "No results.", nil
	}
	d := result.Data
	buckets := []bucket{
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

	for _, b := range buckets {
		if len(b.hits) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "## %s\n\n", b.name)
		for i, h := range b.hits {
			fmt.Fprintf(&sb, "%d. **%s**\n   %s\n", i+1, fallback(h.Title, "(untitled)"), h.URL)
			if snippet := truncate(h.Snippet, snippetMax); snippet != "" {
				fmt.Fprintf(&sb, "   %s\n", snippet)
			}
			if h.Time != "" {
				fmt.Fprintf(&sb, "   _%s_\n", h.Time)
			}
			sb.WriteString("\n")

			items = append(items, SearchResultItem{
				Type:    b.kind,
				Title:   h.Title,
				URL:     h.URL,
				Snippet: h.Snippet,
				Time:    h.Time,
			})
		}
	}

	if sb.Len() == 0 {
		return "No results.", nil
	}
	return strings.TrimRight(sb.String(), "\n"), items
}

func FormatExtract(result *kagi.ExtractResult) (string, []ExtractItem) {
	if result == nil {
		return "No content extracted.", nil
	}

	errByURL := make(map[string]string, len(result.Errors))
	var orphanErrors []kagi.ErrorDetail
	for _, e := range result.Errors {
		url := urlFromLocation(e.Location)
		if url != "" {
			errByURL[url] = formatErrorDetail(e)
		} else {
			orphanErrors = append(orphanErrors, e)
		}
	}

	var sb strings.Builder
	items := make([]ExtractItem, 0, len(result.Data)+len(result.Errors))

	for _, page := range result.Data {
		fmt.Fprintf(&sb, "## %s\n\n", page.URL)
		if page.Markdown != "" {
			sb.WriteString(page.Markdown)
		} else {
			sb.WriteString("_(no content)_")
		}
		sb.WriteString("\n\n")

		item := ExtractItem{URL: page.URL, Markdown: page.Markdown}
		if msg, ok := errByURL[page.URL]; ok {
			item.Error = msg
		}
		items = append(items, item)
	}

	if len(orphanErrors) > 0 {
		sb.WriteString("## Errors\n\n")
		for _, e := range orphanErrors {
			fmt.Fprintf(&sb, "- %s\n", formatErrorDetail(e))
			items = append(items, ExtractItem{URL: urlFromLocation(e.Location), Error: formatErrorDetail(e)})
		}
	}

	if sb.Len() == 0 {
		return "No content extracted.", items
	}
	return strings.TrimRight(sb.String(), "\n"), items
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

// urlFromLocation is a placeholder: ErrorDetail.Location is a field path
// (e.g. "pages[0].url"), not a URL. Resolving back to the URL would require
// correlating with the request; for now we treat all errors as orphan.
func urlFromLocation(_ string) string {
	return ""
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
