package cmdutil

import (
	"net/http"
	"pvmt/internal/db"
	"pvmt/pkg/iostreams"
)

type Factory struct {
	AppVersion     string
	ExecutableName string
	IOStreams       *iostreams.IOStreams
	HttpClient     func() (*http.Client, error)
	DB             func() (db.Store, error)
}
