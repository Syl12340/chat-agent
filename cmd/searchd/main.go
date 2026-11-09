package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"chat-agent/internal/localsearch"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	listen := flag.String("listen", "127.0.0.1:9993", "loopback HTTP JSON/MCP listen address")
	ttl := flag.Duration("cache-ttl", 5*time.Minute, "in-memory query cache TTL; 0 disables")
	rate := flag.Int("upstream-per-minute", 20, "maximum uncached upstream requests per minute")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || host != "127.0.0.1" {
		return errors.New("search daemon must bind to 127.0.0.1")
	}
	service, err := localsearch.New(os.Getenv("CHAT_SEARCH_TOKEN"), *ttl, *rate)
	if err != nil {
		return err
	}
	defer service.Close()
	listener, err := net.Listen("tcp4", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: service.Handler(), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	defer server.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Printf("Chat Search: http://%s (loopback JSON /search and MCP /mcp)\n", listener.Addr())
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(ctx)
	}
}
