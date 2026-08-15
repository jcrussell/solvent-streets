package cmdutil

import (
	"math"
	"testing"
)

func TestHumanSize(t *testing.T) {
	const (
		kib = int64(1) << 10
		mib = int64(1) << 20
		gib = int64(1) << 30
		tib = int64(1) << 40
		pib = int64(1) << 50
		eib = int64(1) << 60
	)
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"}, // exact below 1 KiB, never "1.0 KiB"
		{kib, "1.0 KiB"},
		{kib + kib/2, "1.5 KiB"},
		{mib, "1.0 MiB"},
		{gib, "1.0 GiB"},
		{tib, "1.0 TiB"},
		// The old checksite implementation's suffix table ended at TiB
		// and was indexed without a bound check, so everything from here
		// down panicked with index-out-of-range.
		{pib, "1.0 PiB"},
		{eib, "1.0 EiB"},
		{math.MaxInt64, "8.0 EiB"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := HumanSize(tc.in); got != tc.want {
				t.Errorf("HumanSize(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestHumanSize_NoPanicAcrossMagnitudes sweeps every power of two so a
// future edit to the suffix table cannot reintroduce an unguarded index.
func TestHumanSize_NoPanicAcrossMagnitudes(t *testing.T) {
	for shift := range 63 {
		n := int64(1) << shift
		if got := HumanSize(n); got == "" {
			t.Errorf("HumanSize(1<<%d) returned empty", shift)
		}
	}
}
