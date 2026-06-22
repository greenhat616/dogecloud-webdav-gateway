package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/greenhat616/dogecloud-webdav-gateway/internal/config"
	"github.com/greenhat616/dogecloud-webdav-gateway/internal/doge"
	"github.com/greenhat616/dogecloud-webdav-gateway/internal/gateway"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	httpClient := &http.Client{Timeout: 0}
	dogeClient := doge.NewClient(cfg.DogeAPIBaseURL, httpClient)
	handler := gateway.New(cfg, dogeClient, httpClient)

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("dogecloud webdav gateway listening on %s", cfg.Addr)
		errCh <- server.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	case <-stop:
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("server shutdown: %v", err)
		}
		if err := handler.Shutdown(ctx); err != nil {
			log.Printf("gateway shutdown: %v", err)
		}
	}
}
