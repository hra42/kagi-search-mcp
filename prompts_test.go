package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type promptHandler func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error)

func newReq(args map[string]string) *mcp.GetPromptRequest {
	return &mcp.GetPromptRequest{Params: &mcp.GetPromptParams{Arguments: args}}
}

func TestPromptHandlers_RequiredArgs(t *testing.T) {
	cases := []struct {
		name    string
		handler promptHandler
		argKey  string
	}{
		{"research/topic", researchHandler, "topic"},
		{"fact-check/claim", factCheckHandler, "claim"},
		{"compare-sources/topic", compareSourcesHandler, "topic"},
		{"find-primary-sources/topic", findPrimarySourcesHandler, "topic"},
		{"summarize-url/url", summarizeURLHandler, "url"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/nil-args", func(t *testing.T) {
			if _, err := tc.handler(context.Background(), newReq(nil)); err == nil {
				t.Fatalf("expected error for missing %q, got nil", tc.argKey)
			}
		})
		t.Run(tc.name+"/blank-arg", func(t *testing.T) {
			if _, err := tc.handler(context.Background(), newReq(map[string]string{tc.argKey: "   "})); err == nil {
				t.Fatalf("expected error for blank %q, got nil", tc.argKey)
			}
		})
	}
}

func TestPromptHandlers_HappyPath(t *testing.T) {
	cases := []struct {
		name      string
		handler   promptHandler
		args      map[string]string
		wantInTxt []string // substrings that must appear in the message text
		wantTools []string // tool names that must appear
	}{
		{
			"research",
			researchHandler,
			map[string]string{"topic": "transformer architectures", "depth": "deep"},
			[]string{"transformer architectures"},
			[]string{"kagi_search", "kagi_extract"},
		},
		{
			"fact-check",
			factCheckHandler,
			map[string]string{"claim": "the earth is round"},
			[]string{"the earth is round", "Verdict"},
			[]string{"kagi_search", "kagi_extract"},
		},
		{
			"compare-sources",
			compareSourcesHandler,
			map[string]string{"topic": "rent control", "perspectives": "4"},
			[]string{"rent control"},
			[]string{"kagi_search", "kagi_extract"},
		},
		{
			"find-primary-sources",
			findPrimarySourcesHandler,
			map[string]string{"topic": "CRISPR ethics"},
			[]string{"CRISPR ethics", "site:gov", "filetype:pdf"},
			[]string{"kagi_search"},
		},
		{
			"summarize-url",
			summarizeURLHandler,
			map[string]string{"url": "https://example.com/article"},
			[]string{"https://example.com/article"},
			[]string{"kagi_extract"},
		},
		{
			"summarize-url with focus",
			summarizeURLHandler,
			map[string]string{"url": "https://example.com/article", "focus": "methodology"},
			[]string{"https://example.com/article", "methodology"},
			[]string{"kagi_extract"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tc.handler(context.Background(), newReq(tc.args))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res == nil {
				t.Fatal("nil result")
			}
			if len(res.Messages) != 1 {
				t.Fatalf("want exactly 1 message, got %d", len(res.Messages))
			}
			msg := res.Messages[0]
			if msg.Role != "user" {
				t.Errorf("want role=user, got %q", msg.Role)
			}
			tc_text, ok := msg.Content.(*mcp.TextContent)
			if !ok {
				t.Fatalf("want *mcp.TextContent, got %T", msg.Content)
			}
			for _, want := range tc.wantInTxt {
				if !strings.Contains(tc_text.Text, want) {
					t.Errorf("message text missing %q.\nGot:\n%s", want, tc_text.Text)
				}
			}
			for _, tool := range tc.wantTools {
				if !strings.Contains(tc_text.Text, tool) {
					t.Errorf("message text missing tool reference %q.\nGot:\n%s", tool, tc_text.Text)
				}
			}
		})
	}
}

func TestSummarizeURLHandler_ValidatesURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"http rejected", "http://example.com/x", true},
		{"missing scheme", "example.com/x", true},
		{"missing host", "https://", true},
		{"valid https", "https://example.com/x", false},
		{"valid https with path", "https://news.example.org/2024/article", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := summarizeURLHandler(context.Background(), newReq(map[string]string{"url": tc.url}))
			if (err != nil) != tc.wantErr {
				t.Fatalf("url=%q got err=%v, wantErr=%v", tc.url, err, tc.wantErr)
			}
		})
	}
}

func TestCompareSources_PerspectivesClamp(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 3},
		{"abc", 3},
		{"1", 2},
		{"2", 2},
		{"3", 3},
		{"5", 5},
		{"7", 5},
		{"-2", 2},
	}
	for _, tc := range cases {
		t.Run("in="+tc.in, func(t *testing.T) {
			got := parseClampInt(tc.in, 2, 5, 3)
			if got != tc.want {
				t.Fatalf("parseClampInt(%q)=%d, want %d", tc.in, got, tc.want)
			}
			// And the handler itself should not error.
			if _, err := compareSourcesHandler(context.Background(), newReq(map[string]string{"topic": "x", "perspectives": tc.in})); err != nil {
				t.Fatalf("handler errored on perspectives=%q: %v", tc.in, err)
			}
		})
	}
}

func TestRegisterPrompts_AdvertisesAll(t *testing.T) {
	ctx := context.Background()

	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.0"}, nil)
	registerPrompts(s)

	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	want := map[string]bool{
		"research":             false,
		"fact-check":           false,
		"compare-sources":      false,
		"find-primary-sources": false,
		"summarize-url":        false,
	}
	for p, err := range cs.Prompts(ctx, nil) {
		if err != nil {
			t.Fatalf("Prompts iter: %v", err)
		}
		if _, ok := want[p.Name]; ok {
			want[p.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("prompt %q not advertised", name)
		}
	}
}
