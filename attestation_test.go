package ceremony

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOfflineAttestationRejectsMissingAndCorruptFiles(t *testing.T) {
	s := demoStore(t)
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	_, initial, err := s.Lifecycle()
	must(t, err)
	before, err := s.Snapshot(initial)
	must(t, err)
	i, err := s.InviteQueued(server.URL, "alice", Core, time.Hour)
	must(t, err)
	client := NewClient(t.TempDir())
	client.Progress = io.Discard
	must(t, client.Run(context.Background(), i, Core))
	server.Close()
	i.Expires = time.Now().Add(-time.Hour)
	attestation, err := VerifyLocalContributions(client.Work, i)
	must(t, err)
	if len(attestation.Receipts) != len(i.Jobs) {
		t.Fatal("incomplete attestation")
	}
	must(t, s.VerifyAttestation(attestation))
	if before.VerifyAttestation(attestation) == nil {
		t.Fatal("attested receipts absent from checkpoint")
	}
	omitted := attestation
	omitted.Receipts = attestation.Receipts[:1]
	if s.VerifyAttestation(omitted) == nil {
		t.Fatal("accepted incomplete track attestation")
	}
	tampered := attestation
	tampered.Receipts = append([]Receipt(nil), attestation.Receipts...)
	tampered.Receipts[0].Output.SHA256 = strings.Repeat("0", 64)
	if s.VerifyAttestation(tampered) == nil {
		t.Fatal("accepted a changed attested output")
	}
	data, err := json.Marshal(attestation)
	must(t, err)
	if strings.Contains(string(data), i.Token) || strings.Contains(string(data), i.URL) {
		t.Fatal("attestation leaked invitation data")
	}
	r := attestation.Receipts[0]
	dir := filepath.Join(client.Work, i.ManifestSHA256, i.Contributor, r.Circuit, r.Parent.SHA256)
	receipt := filepath.Join(dir, "receipt.json")
	must(t, os.Rename(receipt, receipt+".saved"))
	if _, err = VerifyLocalContributions(client.Work, i); err == nil {
		t.Fatal("attested a missing receipt")
	}
	must(t, os.Rename(receipt+".saved", receipt))
	output := filepath.Join(dir, "output.ph2")
	f, err := os.OpenFile(output, os.O_WRONLY, 0600)
	must(t, err)
	_, err = f.WriteAt([]byte{0xff}, 0)
	must(t, err)
	must(t, f.Close())
	if _, err = VerifyLocalContributions(client.Work, i); err == nil {
		t.Fatal("attested corrupted output")
	}
}
