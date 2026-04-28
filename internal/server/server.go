package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"pvmt/internal/export"
	"pvmt/pkg/iostreams"
)

type Server struct {
	cities    []export.CityEntry
	port      int
	ios       *iostreams.IOStreams
	cache     sync.Map // key → cached JSON bytes; never invalidated — restart server after data changes
	forecasts sync.Map // city slug → []export.ForecastExport, shared by serveForecastJSON and serveHexCostSummary
}

func New(cities []export.CityEntry, port int, ios *iostreams.IOStreams) *Server {
	return &Server{cities: cities, port: port, ios: ios}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
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

	handler := corsMiddleware(recoveryMiddleware(mux, s.ios.ErrOut))

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", s.port),
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	fmt.Fprintf(s.ios.ErrOut, "Serving on http://localhost:%d\n", s.port)

	// Graceful shutdown: Serve runs in a goroutine; whichever of ctx
	// cancellation or Serve returning first wins.
	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		fmt.Fprintln(s.ios.ErrOut, "\nshutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
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

func recoveryMiddleware(next http.Handler, errOut io.Writer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				fmt.Fprintf(errOut, "panic: %v\n", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
