package server

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

type Server struct {
	cities    []export.CityEntry
	host      string
	port      int
	ios       *iostreams.IOStreams
	cache     sync.Map // key → *jsonThunk (sync.OnceValues wrapper); single-flight, never invalidated — restart server after data changes
	forecasts sync.Map // city slug → *forecastThunk (sync.OnceValue wrapper); shared by serveForecastJSON and serveHexCostSummary

	// indexTemplate and gameTemplate lazily parse the (embedded, deterministic)
	// page templates once for the server's lifetime. Templates no longer depend
	// on the unit system — display units are a client-side preference (frlu) —
	// so a single cached parse per page replaces the former System-keyed maps.
	indexTemplate func() (*template.Template, error)
	gameTemplate  func() (*template.Template, error)

	// indexPages caches the fully rendered index HTML bytes under a single
	// fixed key (the chosen city is deterministic, so one entry suffices —
	// see renderIndex). Same lifetime-cache invariant; HTML, so it can't ride
	// serveJSONCached (which marshals JSON). Value: *indexThunk.
	indexPages sync.Map // "index" → *indexThunk (sync.OnceValues wrapper)

	// gamePages is the /play equivalent of indexPages: the rendered game HTML,
	// cached the same way. Kept as a separate map (rather than reusing
	// indexPages) because the game and index pages are distinct renders that
	// would otherwise collide on the shared "index" key.
	gamePages sync.Map // "game" → *indexThunk (sync.OnceValues wrapper)

	// ReadyFile, if non-empty, receives the listening URL atomically
	// once the TCP listener is bound. Container/test orchestration polls
	// for the file's existence instead of parsing log lines or sleeping.
	ReadyFile string

	// Ready, if non-nil, is closed once the TCP listener is bound and
	// (when set) ReadyFile has been written. In-process callers — chiefly
	// tests — can `<-srv.Ready` instead of polling the filesystem or
	// sleeping; assign before ListenAndServe.
	Ready chan struct{}
}

func New(cities []export.CityEntry, host string, port int, ios *iostreams.IOStreams) *Server {
	return &Server{
		cities:        cities,
		host:          host,
		port:          port,
		ios:           ios,
		indexTemplate: sync.OnceValues(export.ParseIndexTemplate),
		gameTemplate:  sync.OnceValues(export.ParseGameTemplate),
	}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()

	if len(s.cities) == 1 {
		// Single-city mode: serve data at /data/{file} (backward compatible)
		entry := s.cities[0]
		mux.HandleFunc("GET /data/{file}", s.handleDataFile(entry))
		mux.HandleFunc("GET /api/snapshots", s.handleSnapshotsList(entry))
		mux.HandleFunc("GET /play", s.handleGame) // DATA_PREFIX='' → /data/<file>
		mux.HandleFunc("GET /", s.handleIndex)
	} else {
		// Multi-city mode
		mux.HandleFunc("GET /api/cities", s.handleCitiesList)
		mux.HandleFunc("GET /api/cities/{slug}/snapshots", s.handleCitySnapshotsList)
		mux.HandleFunc("GET /cities/{slug}/data/{file}", s.handleCityDataFile)
		// /play renders per-city: the city comes from ?city=<slug> (the dropdown
		// navigates there), defaulting to the first renderable city, and the page
		// fetches its board under /cities/{slug}/data/. See handleGame.
		mux.HandleFunc("GET /play", s.handleGame)
		mux.HandleFunc("GET /", s.handleIndex)
	}

	// WASM assets (shared)
	mux.HandleFunc("GET /wasm_exec.js", s.handleWasmExecJS)
	mux.HandleFunc("GET /pvmt.wasm", s.handleForecastWasm)

	// Application JS extracted from the templates (shared)
	mux.HandleFunc("GET /app.js", s.handleAppJS)
	mux.HandleFunc("GET /game.js", s.handleGameJS)

	handler := recoveryMiddleware(mux, s.ios.ErrOut)

	srv := &http.Server{
		Addr:         net.JoinHostPort(s.host, strconv.Itoa(s.port)),
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
	if s.Ready != nil {
		close(s.Ready)
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
