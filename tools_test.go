package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	kagi "github.com/hra42/kagi-go-sdk"
)

func TestValidateSearchInput(t *testing.T) {
	cases := []struct {
		name    string
		in      SearchInput
		wantErr bool
	}{
		{"empty query", SearchInput{}, true},
		{"whitespace query", SearchInput{Query: "   "}, true},
		{"valid minimal", SearchInput{Query: "go"}, false},
		{"limit too high", SearchInput{Query: "x", Limit: 9999}, true},
		{"page too high", SearchInput{Query: "x", Page: 99}, true},
		{"bad safe_search", SearchInput{Query: "x", SafeSearch: "maybe"}, true},
		{"good safe_search", SearchInput{Query: "x", SafeSearch: "on"}, false},
		{"bad workflow", SearchInput{Query: "x", Workflow: "audiobooks"}, true},
		{"good workflow", SearchInput{Query: "x", Workflow: "news"}, false},
		{"bad response_format", SearchInput{Query: "x", ResponseFormat: "verbose"}, true},
		{"good response_format concise", SearchInput{Query: "x", ResponseFormat: "concise"}, false},
		{"good response_format detailed", SearchInput{Query: "x", ResponseFormat: "detailed"}, false},
		{"bad fields", SearchInput{Query: "x", Fields: []string{"web", "bogus"}}, true},
		{"good fields", SearchInput{Query: "x", Fields: []string{"web", "news"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := validateSearchInput(tc.in)
			if (msg != "") != tc.wantErr {
				t.Fatalf("validateSearchInput got %q, wantErr=%v", msg, tc.wantErr)
			}
		})
	}
}

func TestValidateExtractInput(t *testing.T) {
	cases := []struct {
		name    string
		in      ExtractInput
		wantErr bool
	}{
		{"empty", ExtractInput{}, true},
		{"single https", ExtractInput{URLs: []string{"https://a.example/x"}}, false},
		{"http not allowed", ExtractInput{URLs: []string{"http://a.example/x"}}, true},
		{"malformed", ExtractInput{URLs: []string{"not a url"}}, true},
		{"missing host", ExtractInput{URLs: []string{"https://"}}, true},
		{"too many", ExtractInput{URLs: makeURLs(11)}, true},
		{"timeout out of range", ExtractInput{URLs: []string{"https://a.example"}, Timeout: 99}, true},
		{"timeout in range", ExtractInput{URLs: []string{"https://a.example"}, Timeout: 5}, false},
		{"timeout zero ok", ExtractInput{URLs: []string{"https://a.example"}, Timeout: 0}, false},
		{"max_chars negative", ExtractInput{URLs: []string{"https://a.example"}, MaxChars: -1}, true},
		{"max_chars positive", ExtractInput{URLs: []string{"https://a.example"}, MaxChars: 4000}, false},
		{"max_chars zero", ExtractInput{URLs: []string{"https://a.example"}, MaxChars: 0}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := validateExtractInput(tc.in)
			if (msg != "") != tc.wantErr {
				t.Fatalf("validateExtractInput got %q, wantErr=%v", msg, tc.wantErr)
			}
		})
	}
}

func TestClassifyAPIError_Wording(t *testing.T) {
	cases := []struct {
		name           string
		err            error
		wantSubstrings []string
	}{
		{
			"unauthorized has recovery hint",
			&kagi.APIError{StatusCode: 401, Kind: kagi.ErrUnauthorized},
			[]string{"unauthorized", "KAGI_API_KEY", "do not retry"},
		},
		{
			"rate limited with retry-after",
			&kagi.APIError{StatusCode: 429, Kind: kagi.ErrRateLimited, RetryAfter: 5 * time.Second},
			[]string{"rate limited", "5s", "quota"},
		},
		{
			"bad request includes recovery cue",
			&kagi.APIError{StatusCode: 400, Kind: kagi.ErrBadRequest, Details: []kagi.ErrorDetail{{Message: "bad lens"}}},
			[]string{"bad lens", "documented constraints"},
		},
		{
			"server error mentions transient",
			&kagi.APIError{StatusCode: 503, Kind: kagi.ErrServerError},
			[]string{"server error", "transient", "503"},
		},
		{
			"transport error returns empty (propagate as Go error)",
			errors.New("dial tcp: timeout"),
			[]string{}, // expect ""
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyAPIError(tc.err)
			if len(tc.wantSubstrings) == 0 {
				if got != "" {
					t.Fatalf("expected empty (propagate), got %q", got)
				}
				return
			}
			for _, want := range tc.wantSubstrings {
				if !strings.Contains(got, want) {
					t.Errorf("classifyAPIError = %q; want substring %q", got, want)
				}
			}
		})
	}
}

func TestResolveResponseFormatAndFieldSet(t *testing.T) {
	if resolveResponseFormat("") != "concise" {
		t.Fatalf("empty response_format should resolve to concise")
	}
	if resolveResponseFormat("detailed") != "detailed" {
		t.Fatalf("detailed should pass through")
	}
	if fieldSet(nil) != nil || fieldSet([]string{}) != nil {
		t.Fatalf("empty fields should yield nil set (= keep all)")
	}
	got := fieldSet([]string{"web", "news"})
	if _, ok := got["web"]; !ok {
		t.Fatalf("expected web in set")
	}
	if _, ok := got["news"]; !ok {
		t.Fatalf("expected news in set")
	}
}

func makeURLs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "https://a.example/" + string(rune('a'+i))
	}
	return out
}
