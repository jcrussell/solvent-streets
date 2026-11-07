package pvmtcmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/pkg/cmd/factory"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestRun_UnknownServiceNameExitsTwo pins that a service name the registry
// does not know, in [[services]] or in a [[cities.facilities]] entry, is an
// invalid config for every command that reads the config: exit 2 with the
// typo named, not a silent default (config show, services status), a lone
// warning and exit 0 (all compute), or a hand-placed station quietly dropped.
func TestRun_UnknownServiceNameExitsTwo(t *testing.T) {
	const city = "[[cities]]\nname = \"Livermore, CA\"\noverpass = true\n"
	configs := map[string]string{
		"services name":    "[[services]]\nname = \"fier\"\nturnout_s = 60\n\n" + city,
		"facility service": city + "\n  [[cities.facilities]]\n  service = \"fier\"\n  lat = 37.68\n  lon = -121.77\n",
	}
	commands := [][]string{{"config", "show"}, {"config", "show", "--sources"}, {"services", "status"}, {"all", "compute"}}
	for name, doc := range configs {
		for _, args := range commands {
			t.Run(name+"/"+strings.Join(args, " "), func(t *testing.T) {
				dir := t.TempDir()
				for _, v := range []string{"XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME"} {
					t.Setenv(v, filepath.Join(dir, v))
				}
				if err := os.WriteFile(filepath.Join(dir, "pvmt.toml"), []byte(doc), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Chdir(dir)
				f := factory.New()
				ios, _, out, errOut := iostreams.Test()
				f.IOStreams = ios
				t.Cleanup(func() { _ = f.CloseRootDB() })
				if got := run(context.Background(), f, args); got != 2 {
					t.Fatalf("exit code %d, want 2\nstdout: %s\nstderr: %s", got, out, errOut)
				}
				if !strings.Contains(errOut.String(), `"fier"`) {
					t.Errorf("stderr = %q, want it to name the bad service", errOut.String())
				}
			})
		}
	}
}
