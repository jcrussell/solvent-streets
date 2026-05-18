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

	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

type Server struct {
	cities    []export.CityEntry
	port      int
	ios       *iostreams.IOStreams
	cache     sync.Map // key → *jsonThunk (sync.OnceValues wrapper); single-flight, never invalidated — restart server after data changes
	forecasts sync.Map // city slug → *forecastThunk (sync.OnceValue wrapper); shared by serveForecastJSON and serveHexCostSummary

	// ReadyFile, if non-empty, receives the listening URL atomically
	// once the TCP listener is bound. Container/test orchestration polls
	// for the file's existence instead of parsing log lines or sleeping.
	ReadyFile string
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
		mux.HandleFunc("GET /api/snapshots", s.handleSnapshotsList(entry))
		mux.HandleFunc("GET /", s.handleIndex)
	} else {
		// Multi-city mode
		mux.HandleFunc("GET /api/cities", s.handleCitiesList)
		mux.HandleFunc("GET /api/cities/{slug}/snapshots", s.handleCitySnapshotsList)
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

	url := readyURL(ln.Addr())
	if s.ReadyFile != "" {
		if err := cmdutil.WriteFile(s.ReadyFile, []byte(url+"\n"), 0o644); err != nil {
			_ = ln.Close()
			return fmt.Errorf("write ready file: %w", err)
		}
	}

	fmt.Fprintf(s.ios.ErrOut, "Serving on %s\n", url)

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

// readyURL formats the listener address as a URL suitable for the
// human banner and the --ready-file payload. For wildcard binds
// (0.0.0.0 / [::]) we substitute localhost so the value is directly
// usable by a client on the same host.
func readyURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
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
		err := cmdutil.GuardPanic(errOut, func() error {
			next.ServeHTTP(w, r)
			return nil
		})
		if err != nil {
			fmt.Fprintf(errOut, "panic context: %s %s\n", r.Method, r.URL.RequestURI())
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})
}
