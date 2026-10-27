package ceremony

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark/backend/groth16"
	"github.com/consensys/gnark/frontend"
)

func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func demoStore(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ceremony")
	must(t, Demo(dir))
	s, e := Open(dir)
	must(t, e)
	return s
}
func testClient(t *testing.T) *Client { c := NewClient(t.TempDir()); c.Progress = io.Discard; return c }

func TestCatalogScope(t *testing.T) {
	core, e := Select(Core, "")
	must(t, e)
	large, e := Select(Large, "")
	must(t, e)
	if len(core) != 41 || len(large) != 1 || len(Catalog()) != 42 {
		t.Fatal("wrong ceremony scope")
	}
	for _, s := range core {
		if s.Family != "transfer-confidential" && s.Family != "merge" {
			t.Fatalf("excluded circuit in core: %s", s.ID)
		}
	}
	if large[0].Batch != 4999 || large[0].Height != 40 {
		t.Fatal("wrong large circuit")
	}
	if _, e = Select("all", ""); e == nil {
		t.Fatal("large track must be opt-in")
	}
}

func TestHTTPContributionReplayAndExport(t *testing.T) {
	s := demoStore(t)
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	alice, e := s.Invite(server.URL, "alice", Core, time.Hour)
	must(t, e)
	stale, e := s.Invite(server.URL, "stale", Core, time.Hour)
	must(t, e)
	c := testClient(t)
	must(t, c.Run(context.Background(), alice, Core))
	// Re-running after a lost response is idempotent and preserves receipts.
	before, e := s.State("demo_commitment")
	must(t, e)
	must(t, c.Run(context.Background(), alice, Core))
	after, e := s.State("demo_commitment")
	must(t, e)
	if len(after.History) != 1 || after.History[0] != before.History[0] {
		t.Fatal("duplicate contribution advanced history")
	}
	large, e := s.State("demo_large_track")
	must(t, e)
	if len(large.History) != 0 {
		t.Fatal("core advanced large track")
	}
	if e = testClient(t).Run(context.Background(), stale, Core); e == nil {
		t.Fatal("stale contribution accepted")
	}
	bob, e := s.Invite(server.URL, "bob", Core, time.Hour)
	must(t, e)
	must(t, testClient(t).Run(context.Background(), bob, Core))
	big, e := s.Invite(server.URL, "big", Large, time.Hour)
	must(t, e)
	if e = testClient(t).Run(context.Background(), big, Core); e == nil {
		t.Fatal("large track ran without opt-in")
	}
	if e = s.Audit("missing", 64); e == nil {
		t.Fatal("empty audit succeeded")
	}
	must(t, testClient(t).Run(context.Background(), big, Large))
	must(t, s.Audit(Core, 64))
	must(t, s.Audit(Large, 64))
	beacon := bytes.Repeat([]byte{0x42}, 32)
	out := filepath.Join(t.TempDir(), "keys")
	must(t, s.Export(Core, out, beacon, 64))
	var m ExportManifest
	must(t, ReadJSON(filepath.Join(out, "export.json"), &m))
	if !m.InsecureTestSetup {
		t.Fatal("rehearsal marker lost")
	}
	for _, entry := range m.Circuits {
		pk := groth16.NewProvingKey(ecc.BN254)
		vk := groth16.NewVerifyingKey(ecc.BN254)
		must(t, ReadBinary(filepath.Join(out, entry.Circuit.Spec.ID+".pk"), pk))
		must(t, ReadBinary(filepath.Join(out, entry.Circuit.Spec.ID+".vk"), vk))
		ccs, e := ReadR1CS(s.Object(entry.Circuit.R1CS))
		must(t, e)
		assignment := &DemoCircuit{X: 3, Y: 9, WithCommitment: entry.Circuit.Spec.ID == "demo_commitment"}
		w, e := frontend.NewWitness(assignment, ecc.BN254.ScalarField())
		must(t, e)
		public, e := w.Public()
		must(t, e)
		proof, e := groth16.Prove(ccs, pk, w)
		must(t, e)
		must(t, groth16.Verify(proof, vk, public))
		bad, e := frontend.NewWitness(&DemoCircuit{X: 3, Y: 10, WithCommitment: assignment.WithCommitment}, ecc.BN254.ScalarField())
		must(t, e)
		badPublic, e := bad.Public()
		must(t, e)
		if e = groth16.Verify(proof, vk, badPublic); e == nil {
			t.Fatal("invalid public input verified")
		}
	}
	// Repeated finalization of the same head/beacon produces byte-identical keys.
	second := filepath.Join(t.TempDir(), "keys")
	must(t, s.Export(Core, second, beacon, 64))
	var m2 ExportManifest
	must(t, ReadJSON(filepath.Join(second, "export.json"), &m2))
	for j := range m.Circuits {
		if m.Circuits[j].PK != m2.Circuits[j].PK || m.Circuits[j].VK != m2.Circuits[j].VK {
			t.Fatal("key export is not reproducible")
		}
	}
	must(t, s.VerifyExport(out, 64))
	// An immutable key snapshot remains valid after participation continues.
	carol, e := s.Invite(server.URL, "carol", Core, time.Hour)
	must(t, e)
	must(t, testClient(t).Run(context.Background(), carol, Core))
	must(t, s.VerifyExport(out, 64))
	m.Circuits[0].PK = m.Circuits[1].PK
	must(t, WriteJSON(filepath.Join(out, "export.json"), m))
	if e = s.VerifyExport(out, 64); e == nil {
		t.Fatal("substituted exported key accepted")
	}
	// Transcript replay detects a missing accepted contribution object.
	st, e := s.State("demo_commitment")
	must(t, e)
	must(t, os.Remove(s.Object(st.History[0].Output)))
	if e = s.Audit(Core, 64); e == nil {
		t.Fatal("missing transcript accepted")
	}
	failedExport := filepath.Join(t.TempDir(), "failed-export")
	if e = s.Export(Core, failedExport, beacon, 64); e == nil {
		t.Fatal("export accepted an incomplete transcript")
	}
	if _, e = os.Stat(failedExport); !os.IsNotExist(e) {
		t.Fatal("failed export published partial keys")
	}
}

func TestRejectMalformedAndConcurrentBranches(t *testing.T) {
	s := demoStore(t)
	c := s.Manifest.Circuits[0]
	alice, e := s.Invite("http://localhost:8080", "alice", Core, time.Hour)
	must(t, e)
	bob, e := s.Invite("http://localhost:8080", "bob", Core, time.Hour)
	must(t, e)
	path := filepath.Join(t.TempDir(), "bad.ph2")
	must(t, os.WriteFile(path, []byte("garbage"), 0600))
	if _, e = s.Accept(alice.Token, c.Spec.ID, path); e == nil {
		t.Fatal("malformed contribution accepted")
	}
	st, e := s.State(c.Spec.ID)
	must(t, e)
	if st.Head != c.Initial || len(st.History) != 0 {
		t.Fatal("invalid upload advanced head")
	}
	paths := []string{filepath.Join(t.TempDir(), "a.ph2"), filepath.Join(t.TempDir(), "b.ph2")}
	for _, p := range paths {
		must(t, Contribute(s.Object(c.Initial), p, c.Shape))
	}
	// Well-formed points and lengths are insufficient: reject a broken update
	// proof without advancing the accepted head.
	a, e := ReadPhase2(paths[0], c.Shape)
	must(t, e)
	b, e := ReadPhase2(paths[1], c.Shape)
	must(t, e)
	a.Parameters.G1.Delta = b.Parameters.G1.Delta
	must(t, WriteBinary(path, a))
	if _, e = s.Accept(alice.Token, c.Spec.ID, path); e == nil {
		t.Fatal("invalid cryptographic update accepted")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for j, i := range []Invitation{alice, bob} {
		wg.Add(1)
		go func(j int, i Invitation) { defer wg.Done(); _, e := s.Accept(i.Token, c.Spec.ID, paths[j]); errs <- e }(j, i)
	}
	wg.Wait()
	close(errs)
	success := 0
	for e := range errs {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("accepted %d concurrent branches", success)
	}
	// A forged slice length is rejected before gnark allocates its claimed memory.
	data, e := os.ReadFile(paths[0])
	must(t, e)
	binary.BigEndian.PutUint32(data[34:38], ^uint32(0))
	must(t, os.WriteFile(path, data, 0600))
	if _, e = ReadPhase2(path, c.Shape); e == nil {
		t.Fatal("unbounded parameter vector accepted")
	}
	for _, b := range [][]byte{data[:12], append(data, 0)} {
		must(t, os.WriteFile(path, b, 0600))
		if _, e = ReadPhase2(path, c.Shape); e == nil {
			t.Fatal("malformed binary accepted")
		}
	}
}

func TestDownloadResumesAndAuthenticates(t *testing.T) {
	data := bytes.Repeat([]byte("ceremony"), 1000)
	a := Artifact{SHA256: hashBytes(data), Size: int64(len(data))}
	usedRange := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			usedRange = true
		}
		http.ServeContent(w, r, "object", time.Time{}, bytes.NewReader(data))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "input")
	must(t, os.WriteFile(path+".part", data[:1234], 0600))
	c := testClient(t)
	must(t, c.download(context.Background(), server.URL, path, a))
	if !usedRange {
		t.Fatal("did not resume partial download")
	}
	must(t, Check(path, a))
}

func TestExpiredInvitationAndManifestMismatch(t *testing.T) {
	s := demoStore(t)
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	i, e := s.Invite(server.URL, "alice", Core, time.Hour)
	must(t, e)
	i.ManifestSHA256 = hashBytes([]byte("wrong"))
	if e = testClient(t).Run(context.Background(), i, Core); e == nil {
		t.Fatal("wrong manifest accepted")
	}
	i.Expires = time.Now().Add(-time.Second)
	if e = testClient(t).Run(context.Background(), i, Core); e == nil {
		t.Fatal("expired invitation accepted")
	}
	for _, u := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com/path", "https://example.com/?secret=x"} {
		if ValidateURL(u) == nil {
			t.Fatal("unsafe coordinator URL accepted", u)
		}
	}
}

func TestImportLegacyPhase1(t *testing.T) {
	// A small synthetic legacy encoding validates the serialization bridge only.
	// It is not used as a production SRS anywhere in the tests.
	path := filepath.Join(t.TempDir(), "input.ph1")
	srsPath := filepath.Join(t.TempDir(), "test.srs")
	must(t, TestSRS(srsPath, 2))
	srs, e := ReadSRS(srsPath, 4)
	must(t, e)
	must(t, Atomic(path, func(w io.Writer) error {
		if _, e := w.Write([]byte{2, 0, 0}); e != nil {
			return e
		}
		enc := bn254.NewEncoder(w)
		for _, v := range [][]bn254.G1Affine{srs.G1.Tau, srs.G1.AlphaTau, srs.G1.BetaTau} {
			for j := range v {
				if e := enc.Encode(&v[j]); e != nil {
					return e
				}
			}
		}
		for j := range srs.G2.Tau {
			if e := enc.Encode(&srs.G2.Tau[j]); e != nil {
				return e
			}
		}
		return enc.Encode(&srs.G2.Beta)
	}))
	a, e := Digest(path)
	must(t, e)
	out := filepath.Join(t.TempDir(), "out.srs")
	must(t, ImportPH1(path, out, a.SHA256, 4))
	_, e = ReadSRS(out, 4)
	must(t, e)
	if e = ImportPH1(path, out, hashBytes([]byte("wrong")), 4); e == nil {
		t.Fatal("wrong phase1 digest accepted")
	}
}
