// Package checksite implements `pvmt check-site`, a read-only validator over a
// built static-site tree (the output of `make site` / `pvmt export`). It performs the
// publish-readiness audit that once caught a shipped near-zero-paved-area
// geometry bug: structural inventory, dangling-reference detection, WASM
// freshness against this binary's embedded copy, publish hygiene (no host paths,
// emails, or secrets), size constraints, and data reasonableness/regression
// guards. It never touches the database, network, or config.
package checksite

import (
	"context"
	"fmt"

	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

// Options holds the resolved inputs for a check-site run. Only IOStreams is
// pulled from the Factory — this command needs no DB, config, or network.
type Options struct {
	IO     *iostreams.IOStreams
	Dir    string
	Strict bool
}

func NewCmdCheckSite(f *cmdutil.Factory, runF func(context.Context, *Options) error) *cobra.Command {
	opts := &Options{
		IO: f.IOStreams,
	}

	cmd := &cobra.Command{
		Use:   "check-site [dir]",
		Short: "Validate a built static-site tree for publish-readiness",
		Long: `Validate a built static-site tree (the output of 'make site') before
publishing it. This is a read-only audit — it never touches the database,
network, or config — that mechanizes the manual publish-readiness review,
including the check that once caught a shipped near-zero-paved-area geometry
bug.

A current 'pvmt export' writes one tree rooted at the given directory, so the
audit normally runs against that single export; trees that nest one export per
subdirectory (the retired multi-site layout) are still discovered and audited
per subdirectory.

The following checks run over the given directory (default "site"):

  - STRUCTURE: every city carries the expected data files
    (boundary.geojson, hexgrid.geojson, meta.json, forecast_seed.json,
    forecast.json, hex-cost-summary.json, play-hexes.json, scenarios.json)
    and the site root has index.html, .nojekyll, pvmt.wasm, wasm_exec.js,
    app.js, and game.js.
  - REFERENCES: every local asset referenced by index.html (pvmt.wasm,
    wasm_exec.js, local .css/.js) resolves on disk.
  - WASM FRESHNESS: the site's pvmt.wasm and wasm_exec.js match the
    copies embedded in this binary (a stale site fails).
  - HYGIENE: no text file leaks a host path (/home/, /Users/), an author
    email, or an api-key/password/secret token.
  - CONSTRAINTS: no single file exceeds 100 MB, .nojekyll is present and
    zero-byte, and the total tree size is reported (warned over 1 GB).
  - SIZES: where the bytes live — a total per data file name (plus *.html,
    *.wasm, *.js and other buckets), largest first. Each per-city data
    file carries a budget, enforced against the WORST city rather than
    the mean: a mean lets one bloated city hide behind a hundred small
    ones, and adding cities would un-trip an existing breach. So a WARN
    names a single city that is over, and adding cities cannot cause one
    on its own. The mean and tree total ride along as context. Shared
    root assets (*.wasm, *.js, index.html) are a fixed cost and carry no
    budget. Also available as 'make site-report'.
  - REASONABLENESS: every meta.json has a plausible pct_paved (a value
    between 0 and 1%% signals the near-zero-area bug), and every
    forecast.json baseline has monotonically falling PCI and non-decreasing
    deferred backlog year over year.
  - CONSISTENCY: on a nested multi-export tree, a city slug shared across
    exports reports the same paved area in each. (A single-root export has
    one entry per slug, so nothing to cross-check.)

Exits non-zero if any check FAILs. With --strict, warnings fail too.`,
		Example: `  # Validate the default ./site tree
  pvmt check-site

  # Validate a specific built tree
  pvmt check-site ./site

  # Treat warnings as failures (CI gate)
  pvmt check-site --strict`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Dir = "site"
			if len(args) == 1 {
				opts.Dir = args[0]
			}
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runCheckSite(cmd.Context(), opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Strict, "strict", false, "Treat warnings as failures")

	return cmd
}

func runCheckSite(ctx context.Context, opts *Options) error {
	r := newRunner(opts.IO, opts.Strict)

	st, err := discoverSite(opts.Dir)
	if err != nil {
		return err
	}

	// The checks are CPU/IO bound and run sequentially; interleave a
	// cancellation probe so a SIGINT aborts promptly instead of grinding
	// through every remaining check (finding 881e).
	checks := []func(*site){
		r.checkStructure,
		r.checkReferences,
		r.checkWasmFreshness,
		r.checkHygiene,
		r.checkConstraints,
		r.checkSizes,
		r.checkReasonableness,
		r.checkConsistency,
	}
	for _, check := range checks {
		if err := ctx.Err(); err != nil {
			return err
		}
		check(st)
	}

	return r.finish()
}

// runner accumulates pass/warn/fail results and streams a one-line verdict per
// check to the user. finish() prints the summary and returns ErrSilent (which
// the top-level runner maps to exit 1 without reprinting) when anything failed.
type runner struct {
	io     *iostreams.IOStreams
	strict bool
	pass   int
	warn   int
	fail   int
}

func newRunner(io *iostreams.IOStreams, strict bool) *runner {
	return &runner{io: io, strict: strict}
}

func (r *runner) passf(format string, a ...any) {
	r.pass++
	fmt.Fprintf(r.io.Out, "PASS  %s\n", fmt.Sprintf(format, a...))
}

func (r *runner) warnf(format string, a ...any) {
	r.warn++
	fmt.Fprintf(r.io.Out, "WARN  %s\n", fmt.Sprintf(format, a...))
}

func (r *runner) failf(format string, a ...any) {
	r.fail++
	fmt.Fprintf(r.io.Out, "FAIL  %s\n", fmt.Sprintf(format, a...))
}

func (r *runner) finish() error {
	fmt.Fprintf(r.io.Out, "\n%d passed, %d warnings, %d failed\n", r.pass, r.warn, r.fail)
	if r.fail > 0 {
		return cmdutil.ErrSilent
	}
	if r.strict && r.warn > 0 {
		fmt.Fprintln(r.io.Out, "--strict: warnings treated as failures")
		return cmdutil.ErrSilent
	}
	return nil
}
