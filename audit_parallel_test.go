package ceremony

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestParallelAuditPreservesChecksAndReportsCompletion(t *testing.T) {
	s, _, _ := operatorFixture(t)
	var progress bytes.Buffer
	must(t, s.AuditWithOptions(Core, 64, AuditOptions{Workers: 2, Progress: &progress}))
	if !strings.Contains(progress.String(), "Audit verified: 2/2") {
		t.Fatal("audit completion missing")
	}
	for _, workers := range []int{0, -1, 42} {
		if s.AuditWithOptions(Core, 64, AuditOptions{Workers: workers}) == nil {
			t.Fatal("unbounded worker count accepted")
		}
	}
	c := s.Manifest.Circuits[1]
	data, err := os.ReadFile(s.Object(c.Initial))
	must(t, err)
	data[0] ^= 1
	must(t, os.WriteFile(s.Object(c.Initial), data, 0600))
	if s.AuditWithOptions(Core, 64, AuditOptions{Workers: 2}) == nil {
		t.Fatal("parallel audit accepted changed initialization")
	}
}
