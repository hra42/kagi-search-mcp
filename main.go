package main

import (
	"context"
	"crypto/subtle"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	kagi "github.com/hra42/kagi-go-sdk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	httpAddr := flag.String("http", "", "listen address for Streamable HTTP transport (e.g. :8080 or 127.0.0.1:8080); empty = stdio")
	httpPath := flag.String("http-path", "/mcp", "URL path for the MCP endpoint when --http is set")
	flag.Parse()

	apiKey := os.Getenv("KAGI_API_KEY")
	if apiKey == "" {
		log.Fatal("KAGI_API_KEY environment variable is required")
	}

	client := kagi.NewClient(apiKey)

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "kagi-search-mcp",
		Version: "0.1.0",
	}, nil)
	registerTools(server, client)

	if *httpAddr == "" {
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatalf("server stopped: %v", err)
		}
		return
	}

	runHTTP(server, *httpAddr, *httpPath)
}

func runHTTP(server *mcp.Server, addr, path string) {
	token := os.Getenv("MCP_AUTH_TOKEN")
	if token == "" {
		log.Fatal("MCP_AUTH_TOKEN environment variable is required when running with --http")
	}

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		nil,
	)

	mux := http.NewServeMux()
	mux.Handle(path, bearerAuth(token, handler))

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("kagi-search-mcp listening on %s%s (Streamable HTTP)", addr, path)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http server stopped: %v", err)
	}
}

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
