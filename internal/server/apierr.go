package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/recovery"
)

// apiPrefix is the path every API route lives under.
const apiPrefix = "/api/"

// apiHandler serves the API mux with every error response JSON
// (jsonErrors): a handler's, the mux's own 404 and 405, and a panic's 500.
func apiHandler(errOut io.Writer, api http.Handler) http.Handler {
	return jsonErrors(func(w http.ResponseWriter, r *http.Request) {
		if err := recovery.Guard(errOut, func() error { api.ServeHTTP(w, r); return nil }); err != nil {
			fmt.Fprintf(errOut, "panic context: %s %s\n", r.Method, r.URL.RequestURI())
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}
	})
}

// jsonErrors makes next's error responses JSON: a status of 400 or more gets
// the body {"error": "<message>"} (Content-Type application/json) in place
// of the plain-text one http.Error writes, so an /api client reads every
// response as JSON. Success responses pass through untouched. The /data
// files keep plain-text errors, like the static host they stand in for.
func jsonErrors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jw := &jsonErrorWriter{ResponseWriter: w}
		next(jw, r)
		jw.finish()
	}
}

// jsonErrorWriter holds back an error response's body until the handler
// returns, then writes it as the JSON envelope.
type jsonErrorWriter struct {
	http.ResponseWriter
	code int
	body bytes.Buffer
}

func (w *jsonErrorWriter) WriteHeader(code int) {
	if code < http.StatusBadRequest {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.code = code
}

func (w *jsonErrorWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		return w.ResponseWriter.Write(b)
	}
	return w.body.Write(b)
}

func (w *jsonErrorWriter) finish() {
	if w.code == 0 {
		return
	}
	msg := strings.TrimSpace(w.body.String())
	if msg == "" {
		msg = http.StatusText(w.code)
	}
	out, _ := json.Marshal(map[string]string{"error": msg})
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Del("Content-Length")
	w.ResponseWriter.WriteHeader(w.code)
	_, _ = w.ResponseWriter.Write(append(out, '\n'))
}
