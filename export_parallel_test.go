package ceremony

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParallelExportPreservesBytesAndRejectsTampering(t *testing.T) {
	s, operator, _ := operatorFixture(t)
	i, err := operator.Invite(context.Background(), "alice", 24)
	must(t, err)
	client := NewClient(t.TempDir())
	client.Progress = io.Discard
	must(t, client.Run(context.Background(), i, Core))
	beacon := bytes.Repeat([]byte{0x42}, 32)
	serial := filepath.Join(t.TempDir(), "serial")
	parallel := filepath.Join(t.TempDir(), "parallel")
	must(t, s.Export(Core, serial, beacon, 64))
	var progress bytes.Buffer
	options := AuditOptions{Workers: 2, Progress: &progress}
	must(t, s.ExportWithOptions(Core, parallel, beacon, 64, options))
	if !strings.Contains(progress.String(), "Audit verified: 2/2") {
		t.Fatal("export completion missing")
	}
	entries, err := os.ReadDir(serial)
	must(t, err)
	for _, entry := range entries {
		a, err := Digest(filepath.Join(serial, entry.Name()))
		must(t, err)
		must(t, Check(filepath.Join(parallel, entry.Name()), a))
	}
	must(t, s.VerifyExportWithOptions(serial, 64, options))
	must(t, s.VerifyExport(parallel, 64))
	key := filepath.Join(parallel, s.Manifest.Circuits[0].Spec.ID+".pk")
	data, err := os.ReadFile(key)
	must(t, err)
	data[len(data)-1] ^= 1
	must(t, os.WriteFile(key, data, 0600))
	if s.VerifyExportWithOptions(parallel, 64, options) == nil {
		t.Fatal("parallel verification accepted a changed key")
	}
	// No partial export becomes visible if a worker rejects its initialization.
	initial := s.Object(s.Manifest.Circuits[1].Initial)
	data, err = os.ReadFile(initial)
	must(t, err)
	data[0] ^= 1
	must(t, os.WriteFile(initial, data, 0600))
	failed := filepath.Join(t.TempDir(), "failed")
	if s.ExportWithOptions(Core, failed, beacon, 64, options) == nil {
		t.Fatal("export accepted a changed initialization")
	}
	if _, err = os.Stat(failed); !os.IsNotExist(err) {
		t.Fatal("failed export was published")
	}
}

func TestFinalKeyWorkerBounds(t *testing.T) {
	s, _, _ := operatorFixture(t)
	for _, workers := range []int{-1, 0, 42} {
		options := AuditOptions{Workers: workers}
		if err := s.ExportWithOptions(Core, "", nil, 64, options); err == nil {
			t.Fatal("export accepted invalid worker count")
		}
		if err := s.VerifyExportWithOptions("", 64, options); err == nil {
			t.Fatal("verification accepted invalid worker count")
		}
		if _, err := s.FinalizeWithOptions("", BeaconProof{}, 64, options); err == nil {
			t.Fatal("finalization accepted invalid worker count")
		}
	}
}
