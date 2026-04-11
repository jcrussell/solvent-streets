package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"pvmt/internal/export"
)

type Server struct {
	cities []export.CityEntry
	port   int
	cache  sync.Map // key → cached JSON bytes; never invalidated — restart server after data changes
}

func New(cities []export.CityEntry, port int) *Server {
	return &Server{cities: cities, port: port}
}

func (s *Server) ListenAndServe() error {
	mux := http.NewServeMux()

	if len(s.cities) == 1 {
		// Single-city mode: serve data at /data/{file} (backward compatible)
		entry := s.cities[0]
		mux.HandleFunc("GET /data/{file}", s.handleDataFile(entry))
		mux.HandleFunc("GET /", s.handleIndex)
	} else {
		// Multi-city mode
		mux.HandleFunc("GET /api/cities", s.handleCitiesList)
		mux.HandleFunc("GET /cities/{slug}/data/{file}", s.handleCityDataFile)
		mux.HandleFunc("GET /", s.handleIndex)
	}

	// WASM assets (shared)
	mux.HandleFunc("GET /wasm_exec.js", s.handleWasmExecJS)
	mux.HandleFunc("GET /forecast.wasm", s.handleForecastWasm)

	handler := corsMiddleware(recoveryMiddleware(mux))

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", s.port),
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Serving on http://localhost:%d\n", s.port)

	// Graceful shutdown
	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ln)
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		fmt.Fprintf(os.Stderr, "\nReceived %s, shutting down...\n", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	case err := <-done:
		return err
	}
}

func (s *Server) cityBySlug(slug string) *export.CityEntry {
	for i := range s.cities {
		if s.cities[i].Slug == slug {
			return &s.cities[i]
		}
	}
	return nil
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				fmt.Fprintf(os.Stderr, "panic: %v\n", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
