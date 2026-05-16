package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/time/rate"
)

func TestBearerAuth(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	h := bearerAuth("secret", ok)

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"missing", "", 401},
		{"wrong scheme", "Basic secret", 401},
		{"wrong token", "Bearer nope", 401},
		{"correct", "Bearer secret", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/mcp", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	called := 0
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(200)
	})
	// 0 rps, burst 2 → first 2 pass, rest 429 (per IP).
	limiter := newIPLimiter(rate.Limit(0), 2)
	h := rateLimitMiddleware(limiter, ok)

	var lastCode int
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/mcp", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		lastCode = w.Code
	}
	if called != 2 {
		t.Fatalf("expected 2 passes through to handler, got %d", called)
	}
	if lastCode != http.StatusTooManyRequests {
		t.Fatalf("expected final 429, got %d", lastCode)
	}

	// Different IP gets its own bucket.
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.RemoteAddr = "10.0.0.2:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("second IP should be allowed, got %d", w.Code)
	}
}

func TestRequestIDMiddleware_GeneratesAndEchoes(t *testing.T) {
	var seen string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if v, ok := r.Context().Value(requestIDKey).(string); ok {
			seen = v
		}
	})
	h := requestIDMiddleware(next)

	// Provided header is echoed.
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("X-Request-ID", "abc123")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Header().Get("X-Request-ID") != "abc123" || seen != "abc123" {
		t.Fatalf("provided id not preserved: header=%q ctx=%q", w.Header().Get("X-Request-ID"), seen)
	}

	// Missing header → generated.
	seen = ""
	req = httptest.NewRequest("POST", "/mcp", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Header().Get("X-Request-ID") == "" || seen == "" {
		t.Fatalf("expected generated request id")
	}
}
