package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	kagi "github.com/hra42/kagi-go-sdk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/time/rate"
)

const version = "0.1.0"

// serverInstructions is returned in the Initialize response. MCP clients
// (Claude Code, Claude Desktop, VS Code Copilot, etc.) typically inject this
// into the system prompt at session start, before any tool list or user
// message. Keep it short and workflow-focused.
const serverInstructions = `This server exposes the Kagi search and extract APIs as two read-only tools.

Workflow:
1. kagi_search — find ranked URLs for a query. Returns titles + URLs (and snippets in detailed mode). Snippets are short and not authoritative.
2. kagi_extract — read the full page content (as markdown) for 1–10 of the URLs returned above.

Defaults are optimized for low context usage: kagi_search returns "concise" output (top 5 per bucket, no snippets) unless you ask for response_format: "detailed". Extract truncation is controlled per call via max_chars.

Both tools are read-only and operate on the open web. Errors include specific recovery hints (e.g. retry with higher timeout, omit invalid parameter). Per-URL extract failures are returned alongside successes, not as call-level errors.

The server also exposes prompts (research, fact-check, compare-sources, find-primary-sources, summarize-url) that the user can invoke from their client's prompt menu. Each prompt expands to a structured instruction telling you which of the tools above to call and how to format the answer; follow the returned instructions step-by-step.`

func main() {
	httpAddr := flag.String("http", "", "listen address for Streamable HTTP transport (e.g. 127.0.0.1:8080); empty = stdio")
	httpPath := flag.String("http-path", "/mcp", "URL path for the MCP endpoint when --http is set")
	flag.Parse()

	logger := newLogger()
	slog.SetDefault(logger)

	apiKey := os.Getenv("KAGI_API_KEY")
	if apiKey == "" {
		logger.Error("missing required env var", "var", "KAGI_API_KEY")
		os.Exit(1)
	}

	snippetMax := envInt("KAGI_SNIPPET_MAX", defaultSnippetMax)
	maxOutputChars := envInt("KAGI_MAX_OUTPUT_CHARS", defaultMaxOutputChars)

	client := kagi.NewClient(apiKey)

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "kagi-search-mcp",
		Version: version,
	}, &mcp.ServerOptions{
		Instructions: serverInstructions,
	})
	registerTools(server, client, snippetMax, maxOutputChars)
	registerPrompts(server)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *httpAddr == "" {
		logger.Info("starting kagi-search-mcp",
			"version", version, "transport", "stdio",
			"snippet_max", snippetMax, "max_output_chars", maxOutputChars,
		)
		if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("server stopped", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := runHTTP(ctx, logger, server, *httpAddr, *httpPath, snippetMax, maxOutputChars); err != nil {
		logger.Error("http server stopped", "err", err)
		os.Exit(1)
	}
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	if v := strings.ToLower(os.Getenv("LOG_LEVEL")); v != "" {
		switch v {
		case "debug":
			level = slog.LevelDebug
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func runHTTP(ctx context.Context, logger *slog.Logger, server *mcp.Server, addr, path string, snippetMax, maxOutputChars int) error {
	token := os.Getenv("MCP_AUTH_TOKEN")
	if token == "" {
		return errors.New("MCP_AUTH_TOKEN environment variable is required when running with --http")
	}

	rps := envFloat("MCP_RATE_RPS", 5)
	burst := envInt("MCP_RATE_BURST", 20)

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		nil,
	)

	limiter := newIPLimiter(rate.Limit(rps), burst)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if os.Getenv("KAGI_API_KEY") == "" {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	chain := recoverMiddleware(logger,
		requestIDMiddleware(
			accessLogMiddleware(logger,
				rateLimitMiddleware(limiter,
					bearerAuth(token, handler)))))
	mux.Handle(path, chain)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	logger.Info("starting kagi-search-mcp",
		"version", version,
		"transport", "http",
		"addr", addr, "path", path,
		"rate_rps", rps, "rate_burst", burst,
		"snippet_max", snippetMax,
		"max_output_chars", maxOutputChars,
	)

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// --- middleware ---

func bearerAuth(expected string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		got := r.Header.Get("Authorization")
		if !strings.HasPrefix(got, prefix) || subtle.ConstantTimeCompare([]byte(got[len(prefix):]), []byte(expected)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="kagi-search-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type ctxKey int

const requestIDKey ctxKey = 1

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

func recoverMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rv := recover(); rv != nil {
				logger.Error("panic in handler", "err", rv, "path", r.URL.Path)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func accessLogMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		// Intentionally omit query string and Authorization — query is PII-adjacent.
		logger.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", r.Context().Value(requestIDKey),
			"remote", clientIP(r),
		)
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// --- rate limiter (per remote IP) ---

type ipLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	rps      rate.Limit
	burst    int
}

func newIPLimiter(rps rate.Limit, burst int) *ipLimiter {
	return &ipLimiter{limiters: make(map[string]*rate.Limiter), rps: rps, burst: burst}
}

func (l *ipLimiter) get(ip string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	lim, ok := l.limiters[ip]
	if !ok {
		lim = rate.NewLimiter(l.rps, l.burst)
		l.limiters[ip] = lim
	}
	return lim
}

func rateLimitMiddleware(limiter *ipLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.get(clientIP(r)).Allow() {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- env helpers ---

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return def
	}
	return f
}
