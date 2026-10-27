package ceremony

import (
	"os"
	"path/filepath"
	"testing"
)

// Run once against an initialized real-circuit rehearsal. This measures the
// participant's local contribution and verification, excluding network transfer.
func BenchmarkCoreContribution(b *testing.B) {
	dir := os.Getenv("SPR_BENCHMARK_DIR")
	id := os.Getenv("SPR_BENCHMARK_CIRCUIT")
	if dir == "" || id == "" {
		b.Skip("set SPR_BENCHMARK_DIR and SPR_BENCHMARK_CIRCUIT")
	}
	s, err := Open(dir)
	if err != nil {
		b.Fatal(err)
	}
	if !s.Manifest.Phase1.InsecureTestSetup {
		b.Fatal("benchmark requires a marked rehearsal")
	}
	c, err := s.circuit(id)
	if err != nil {
		b.Fatal(err)
	}
	if c.Spec.Track != Core {
		b.Fatal("benchmark is limited to core")
	}
	if err = Check(s.Object(c.Initial), c.Initial); err != nil {
		b.Fatal(err)
	}
	output := filepath.Join(b.TempDir(), "contribution.ph2")
	b.SetBytes(c.Initial.Size)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err = Contribute(s.Object(c.Initial), output, c.Shape); err != nil {
			b.Fatal(err)
		}
	}
}
