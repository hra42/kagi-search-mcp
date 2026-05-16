package main

import (
	"strings"
	"testing"

	kagi "github.com/hra42/kagi-go-sdk"
)

func detailedOpts(snippetMax int) formatOptions {
	return formatOptions{snippetMax: snippetMax, responseFormat: "detailed"}
}

func conciseOpts() formatOptions {
	return formatOptions{responseFormat: "concise"}
}

func TestFormatSearch_NilAndEmpty(t *testing.T) {
	text, items := FormatSearch(nil, detailedOpts(0))
	if !strings.HasPrefix(text, "No results.") || items != nil {
		t.Fatalf("nil input: got (%q, %v)", text, items)
	}
	if !strings.Contains(text, "broaden the query") {
		t.Fatalf("expected recovery hint for no-results case")
	}

	text, items = FormatSearch(&kagi.SearchResult{}, detailedOpts(0))
	if !strings.HasPrefix(text, "No results.") || items != nil {
		t.Fatalf("empty data: got (%q, %v)", text, items)
	}
}

func TestFormatSearch_DetailedBucketsAndTruncation(t *testing.T) {
	longSnippet := strings.Repeat("x", 500)
	r := &kagi.SearchResult{
		Data: kagi.SearchData{
			Search: []kagi.SearchHit{
				{URL: "https://a.example", Title: "A", Snippet: longSnippet, Time: "2026-05-16"},
			},
			News: []kagi.SearchHit{
				{URL: "https://b.example", Title: "", Snippet: "short"},
			},
		},
	}
	text, items := FormatSearch(r, detailedOpts(50))
	if !strings.Contains(text, "## Web") || !strings.Contains(text, "## News") {
		t.Fatalf("missing bucket headers: %s", text)
	}
	if !strings.Contains(text, strings.Repeat("x", 50)+"…") {
		t.Fatalf("expected truncated snippet with ellipsis")
	}
	if !strings.Contains(text, "(untitled)") {
		t.Fatalf("expected untitled fallback for empty title")
	}
	if !strings.Contains(text, "kagi_extract") {
		t.Fatalf("expected trailing next-step hint to mention kagi_extract")
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Snippet != longSnippet {
		t.Fatalf("items[0].Snippet should be untruncated")
	}
}

func TestFormatSearch_ConciseCompactsAndSuppresses(t *testing.T) {
	hits := make([]kagi.SearchHit, 8) // > conciseResultsPerBkt (5)
	for i := range hits {
		hits[i] = kagi.SearchHit{URL: "https://x.example", Title: "T", Snippet: "should not appear"}
	}
	r := &kagi.SearchResult{Data: kagi.SearchData{Search: hits}}
	text, items := FormatSearch(r, conciseOpts())

	if strings.Contains(text, "should not appear") {
		t.Fatalf("concise mode must not include snippets: %s", text)
	}
	if !strings.Contains(text, "3 more Web results suppressed") {
		t.Fatalf("expected suppression footer counting 3: %s", text)
	}
	if !strings.Contains(text, "response_format: \"detailed\"") {
		t.Fatalf("expected suppression footer to mention detailed mode")
	}
	if len(items) != conciseResultsPerBkt {
		t.Fatalf("expected %d items in concise mode, got %d", conciseResultsPerBkt, len(items))
	}
}

func TestFormatSearch_FieldsFilter(t *testing.T) {
	r := &kagi.SearchResult{Data: kagi.SearchData{
		Search: []kagi.SearchHit{{URL: "https://a", Title: "web"}},
		News:   []kagi.SearchHit{{URL: "https://b", Title: "news"}},
	}}
	opts := detailedOpts(0)
	opts.fields = map[string]struct{}{"news": {}}
	text, items := FormatSearch(r, opts)

	if strings.Contains(text, "## Web") {
		t.Fatalf("Web bucket should be filtered out: %s", text)
	}
	if !strings.Contains(text, "## News") {
		t.Fatalf("News bucket should remain: %s", text)
	}
	if len(items) != 1 || items[0].Type != "news" {
		t.Fatalf("expected only news item, got %+v", items)
	}
}

func TestFormatSearch_SnippetMaxDefault(t *testing.T) {
	long := strings.Repeat("y", defaultSnippetMax+10)
	r := &kagi.SearchResult{Data: kagi.SearchData{Search: []kagi.SearchHit{{URL: "u", Title: "t", Snippet: long}}}}
	text, _ := FormatSearch(r, detailedOpts(0)) // 0 → default
	if !strings.Contains(text, strings.Repeat("y", defaultSnippetMax)+"…") {
		t.Fatalf("expected default snippet truncation at %d", defaultSnippetMax)
	}
}

func TestFormatSearch_OutputCapAppendsFooter(t *testing.T) {
	huge := strings.Repeat("z", 500)
	hits := make([]kagi.SearchHit, 50)
	for i := range hits {
		hits[i] = kagi.SearchHit{URL: "https://a.example", Title: "T", Snippet: huge}
	}
	r := &kagi.SearchResult{Data: kagi.SearchData{Search: hits}}
	opts := detailedOpts(500)
	opts.maxOutputChars = 1000
	text, _ := FormatSearch(r, opts)
	if len(text) > 1300 { // 1000 + footer
		t.Fatalf("output not capped; got %d chars", len(text))
	}
	if !strings.Contains(text, "Output truncated") {
		t.Fatalf("expected truncation footer: %q", text[max(0, len(text)-200):])
	}
	if !strings.Contains(text, "response_format") {
		t.Fatalf("expected recovery hint mentioning response_format")
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func TestFormatExtract_ErrorCorrelationByLocation(t *testing.T) {
	requested := []string{"https://ok.example", "https://bad.example"}
	r := &kagi.ExtractResult{
		Data: []kagi.PageResult{
			{URL: "https://ok.example", Markdown: "# hello"},
		},
		Errors: []kagi.ErrorDetail{
			{Code: "extract.timeout", Message: "took too long", Location: "pages[1].url"},
		},
	}
	text, items := FormatExtract(r, requested, formatOptions{})
	if !strings.Contains(text, "# hello") {
		t.Fatalf("missing extracted markdown: %s", text)
	}
	if !strings.Contains(text, "https://bad.example") {
		t.Fatalf("expected bad URL to appear with its error in output: %s", text)
	}
	var ok, bad *ExtractItem
	for i := range items {
		switch items[i].URL {
		case "https://ok.example":
			ok = &items[i]
		case "https://bad.example":
			bad = &items[i]
		}
	}
	if ok == nil || ok.Markdown != "# hello" || ok.Error != "" {
		t.Fatalf("ok item wrong: %+v", ok)
	}
	if bad == nil || bad.Error == "" || !strings.Contains(bad.Error, "extract.timeout") {
		t.Fatalf("bad item wrong: %+v", bad)
	}
	// Recovery hint should be appended.
	if !strings.Contains(bad.Error, "higher `timeout`") {
		t.Fatalf("expected timeout recovery hint on item.Error: %q", bad.Error)
	}
}

func TestFormatExtract_OrphanError(t *testing.T) {
	r := &kagi.ExtractResult{
		Errors: []kagi.ErrorDetail{
			{Code: "extract.unknown", Message: "boom", Location: "not-a-page"},
		},
	}
	text, items := FormatExtract(r, []string{"https://x.example"}, formatOptions{})
	if !strings.Contains(text, "## Errors") {
		t.Fatalf("expected orphan errors section: %s", text)
	}
	if len(items) != 1 || items[0].URL != "" {
		t.Fatalf("expected single orphan item without URL, got %+v", items)
	}
}

func TestFormatExtract_PerURLTruncation(t *testing.T) {
	long := strings.Repeat("m", 200)
	r := &kagi.ExtractResult{Data: []kagi.PageResult{{URL: "https://a.example", Markdown: long}}}
	text, items := FormatExtract(r, []string{"https://a.example"}, formatOptions{perURLMaxChars: 50})

	if strings.Count(text, "m") > 60 { // 50 + small slack from other m's
		t.Fatalf("page not truncated to 50 chars; got %d m's", strings.Count(text, "m"))
	}
	if !strings.Contains(text, "page truncated; 150 more characters") {
		t.Fatalf("expected per-page truncation footer naming the cut size: %s", text)
	}
	// Structured item preserves the FULL markdown (no truncation in the data field).
	if len(items) != 1 || items[0].Markdown != long {
		t.Fatalf("structured item markdown should be untruncated; got len=%d want %d", len(items[0].Markdown), len(long))
	}
}

func TestFormatExtract_AllFailuresAddsGlobalHint(t *testing.T) {
	r := &kagi.ExtractResult{
		Errors: []kagi.ErrorDetail{
			{Code: "extract.timeout", Message: "slow", Location: "pages[0].url"},
		},
	}
	text, _ := FormatExtract(r, []string{"https://a.example"}, formatOptions{})
	if !strings.Contains(text, "every URL failed") {
		t.Fatalf("expected all-failures hint: %s", text)
	}
}

func TestUrlFromLocation(t *testing.T) {
	urls := []string{"a", "b", "c"}
	cases := map[string]string{
		"pages[0].url": "a",
		"pages[2].url": "c",
		"pages[2]":     "c",
		"pages[3].url": "", // out of range
		"foo":          "",
		"":             "",
	}
	for in, want := range cases {
		if got := urlFromLocation(in, urls); got != want {
			t.Errorf("urlFromLocation(%q) = %q, want %q", in, got, want)
		}
	}
}
