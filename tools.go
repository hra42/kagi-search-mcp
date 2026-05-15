package main

import (
	"context"
	"fmt"

	kagi "github.com/hra42/kagi-go-sdk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type SearchInput struct {
	Query      string `json:"query" jsonschema:"the search query"`
	Limit      int    `json:"limit,omitempty" jsonschema:"number of results to return (1-1024)"`
	Page       int    `json:"page,omitempty" jsonschema:"page number (1-10)"`
	SafeSearch string `json:"safe_search,omitempty" jsonschema:"on or off; omit to inherit server default"`
	Workflow   string `json:"workflow,omitempty" jsonschema:"search, images, videos, news, or podcasts"`
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
	URLs    []string `json:"urls" jsonschema:"URLs to extract content from (1-10, HTTPS only)"`
	Timeout float64  `json:"timeout,omitempty" jsonschema:"overall timeout in seconds (0.5-10)"`
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

func registerTools(server *mcp.Server, client *kagi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "kagi_search",
		Description: "Search the web with Kagi. Returns ranked results grouped by category (web, news, images, videos, podcasts, etc.).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		req := kagi.SearchRequest{Query: in.Query}
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
			return nil, SearchOutput{}, err
		}

		text, items := FormatSearch(result)
		out := SearchOutput{Text: text, Results: items}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "kagi_extract",
		Description: "Extract page content as markdown from one or more URLs (1-10, HTTPS).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ExtractInput) (*mcp.CallToolResult, ExtractOutput, error) {
		if len(in.URLs) == 0 {
			return nil, ExtractOutput{}, fmt.Errorf("urls must contain at least one URL")
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
			return nil, ExtractOutput{}, err
		}

		text, items := FormatExtract(result)
		out := ExtractOutput{Text: text, Items: items}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, out, nil
	})
}
