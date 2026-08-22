package filter

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"

	_ "modernc.org/sqlite"
)

// TestCorpusJurisdictionDump histograms ClassifyJurisdiction over a real
// ingested database, so a rule change can be checked against measured movement
// instead of reasoning. It is skipped unless PVMT_CORPUS_DB names a database:
//
//	PVMT_CORPUS_DB=~/.local/share/pvmt/pvmt.db go test ./internal/filter -run Corpus -v
//
// Run it before and after a change and diff the output. This is the gate that
// solvent-streets-qwfo was built behind, and the reason it exists as a
// committed test rather than a throwaway script: every rule in jurisdiction.go
// is seeded from counts like these, and the next one will need the same dump.
//
// Reads only; opens the database read-only so it cannot disturb a live one.
func TestCorpusJurisdictionDump(t *testing.T) {
	path := os.Getenv("PVMT_CORPUS_DB")
	if path == "" {
		t.Skip("set PVMT_CORPUS_DB to a pvmt.db to histogram jurisdiction over a real corpus")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query(`
		SELECT ci.name, f.tags, COUNT(*)
		FROM features f JOIN cities ci ON ci.id = f.city_id
		WHERE f.resource_type = 'roads'
		GROUP BY ci.name, f.tags`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	type cityCounts map[Jurisdiction]int
	byCity := map[string]cityCounts{}
	total := cityCounts{}
	var scanned int

	for rows.Next() {
		var city, tagsJSON string
		var n int
		if err := rows.Scan(&city, &tagsJSON, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		var tags map[string]string
		if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
			continue // a feature with unparseable tags is not this test's problem
		}
		j := ClassifyJurisdiction(tags)
		if byCity[city] == nil {
			byCity[city] = cityCounts{}
		}
		byCity[city][j] += n
		total[j] += n
		scanned += n
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	cities := make([]string, 0, len(byCity))
	for c := range byCity {
		cities = append(cities, c)
	}
	sort.Strings(cities)

	// Tab-separated and sorted so two runs diff cleanly.
	t.Logf("city\tcity_n\tcounty_n\tstate_n\tfederal_n")
	for _, c := range cities {
		cc := byCity[c]
		t.Logf("%s\t%d\t%d\t%d\t%d", c,
			cc[JurisdictionCity], cc[JurisdictionCounty],
			cc[JurisdictionState], cc[JurisdictionFederal])
	}
	t.Logf("TOTAL\t%d\t%d\t%d\t%d  (%d road features across %d cities)",
		total[JurisdictionCity], total[JurisdictionCounty],
		total[JurisdictionState], total[JurisdictionFederal],
		scanned, len(byCity))

	if scanned == 0 {
		t.Fatalf("no road features in %s -- wrong database?", path)
	}
	fmt.Fprintf(os.Stderr, "corpus dump: %d features, %d cities\n", scanned, len(byCity))
}
