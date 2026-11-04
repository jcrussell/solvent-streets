package export

// Metric producer packages whose specs register themselves in init(). The
// exporter must link every one: a producer it never imports would silently
// drop its keys from hex-metrics.json and the page config. internal/services
// is imported for use elsewhere in this package.
import (
	_ "github.com/jcrussell/solvent-streets/internal/population"
)
