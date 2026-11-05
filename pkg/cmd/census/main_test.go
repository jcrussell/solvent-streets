package census

import (
	"testing"

	"github.com/jcrussell/solvent-streets/internal/offlinetest"
)

// No test in this package may reach a real upstream (solvent-streets-zdv7).
func TestMain(m *testing.M) { offlinetest.Main(m) }
