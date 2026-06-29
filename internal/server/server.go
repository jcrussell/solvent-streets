package server

import (
	"context"
	"errors"
	"fmt"
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

	// templates caches the parsed index template keyed by units.System. The
	// system is fixed at construction (every CityEntry shares one *Config, so
	// Config.UnitSystem() is server-wide), so this is effectively a single
	// entry; keying by System is defensive in case multi-config wiring ever
	// lets unit systems differ across cities. Value: *templateThunk.
	templates sync.Map // units.System → *templateThunk (sync.OnceValues wrapper)

	// indexPages caches the fully rendered index HTML bytes under a single
	// fixed key (the chosen city is deterministic, so one entry suffices —
	// see renderIndex). Same lifetime-cache invariant; HTML, so it can't ride
	// serveJSONCached (which marshals JSON). Value: *indexThunk.
	indexPages sync.Map // "index" → *indexThunk (sync.OnceValues wrapper)

	// gamePages and gameTemplates are the /play equivalents of indexPages and
	// templates: the rendered game HTML and the parsed game template, cached
	// the same way. Kept as separate maps (rather than reusing indexPages /
	// templates) because the game and index pages are distinct renders that
	// would otherwise collide on the shared "index" / units.System keys.
	gamePages     sync.Map // "game" → *indexThunk (sync.OnceValues wrapper)
	gameTemplates sync.Map // units.System → *templateThunk (sync.OnceValues wrapper)

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
	return &Server{cities: cities, host: host, port: port, ios: ios}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()

	if len(s.cities) == 1 {
		// Single-city mode: serve data at /data/{file} (backward compatible)
		entry := s.cities[0]
		mux.HandleFunc("GET /data/{file}", s.handleDataFile(entry))
		mux.HandleFunc("GET /api/snapshots", s.handleSnapshotsList(entry))
		mux.HandleFunc("GET /play", s.handleGame) // single-city only; see multi-city branch note
		mux.HandleFunc("GET /", s.handleIndex)
	} else {
		// Multi-city mode
		mux.HandleFunc("GET /api/cities", s.handleCitiesList)
		mux.HandleFunc("GET /api/cities/{slug}/snapshots", s.handleCitySnapshotsList)
		mux.HandleFunc("GET /cities/{slug}/data/{file}", s.handleCityDataFile)
		mux.HandleFunc("GET /", s.handleIndex)
		// NOTE: /play is intentionally single-city only. The game page hardcodes
		// DATA_PREFIX='' and fetches /data/<file>, which only exists in the
		// single-city branch; multi-city serves data under /cities/{slug}/data/.
		// Multi-city /play needs a city selector + templated prefix — see the
		// "multi-city /play" follow-up bead.
	}

	// WASM assets (shared)
	mux.HandleFunc("GET /wasm_exec.js", s.handleWasmExecJS)
	mux.HandleFunc("GET /forecast.wasm", s.handleForecastWasm)

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
