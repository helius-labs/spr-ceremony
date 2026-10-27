package main

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark/backend/groth16"
	groth "github.com/consensys/gnark/backend/groth16/bn254"
	"github.com/consensys/gnark/backend/witness"
	"github.com/consensys/gnark/constraint"
	gnarkio "github.com/consensys/gnark/io"
	ceremony "github.com/helius-labs/spr-ceremony"
	"github.com/helius-labs/zolana/prover/server/prover/common"
)

func check(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}

// Enabled by scripts/test-zolana.sh. Exercises the actual Zolana compilers and
// valid Zolana witnesses, not representative replacement circuits.
func TestZolanaRoundTrip(t *testing.T) {
	revision := os.Getenv("SPR_SOURCE_REVISION")
	fixtures := os.Getenv("SPR_WITNESSES")
	srsPath := os.Getenv("SPR_TEST_SRS")
	if fixtures == "" || srsPath == "" {
		t.Skip("run scripts/test-zolana.sh against a Zolana checkout")
	}
	if len(revision) != 40 {
		t.Fatal("SPR_SOURCE_REVISION must identify the frozen checkout")
	}
	var info ceremony.SRSInfo
	check(t, ceremony.ReadJSON(srsPath+".json", &info))
	if !info.InsecureTestSetup {
		t.Fatal("integration test requires rehearsal SRS")
	}
	var specs []ceremony.Spec
	for _, id := range []string{"transfer_confidential_1_2", "merge_8_1"} {
		selected, e := ceremony.Select(ceremony.Core, id)
		check(t, e)
		specs = append(specs, selected[0])
	}
	if os.Getenv("SPR_CORE_ALL") == "1" {
		var e error
		specs, e = ceremony.Select(ceremony.Core, "")
		check(t, e)
	}
	for _, spec := range specs {
		t.Run(spec.ID, func(t *testing.T) {
			if os.Getenv("SPR_CORE_ALL") == "1" {
				t.Parallel()
			}
			started := time.Now()
			root := t.TempDir()
			if persistent := os.Getenv("SPR_REHEARSAL_DIR"); persistent != "" {
				root = filepath.Join(persistent, spec.ID)
				check(t, os.MkdirAll(root, 0700))
			}
			t.Log("compiling pinned Zolana circuit")
			var ccs constraint.ConstraintSystem
			var e error
			if compiled := os.Getenv("SPR_R1CS_DIR"); compiled != "" {
				var metadata ceremony.Compilation
				check(t, ceremony.ReadJSON(filepath.Join(compiled, "compilation.json"), &metadata))
				if metadata.Engine != ceremony.Engine || metadata.SourceRevision != revision {
					t.Fatal("precompiled circuit provenance mismatch")
				}
				found := false
				for _, entry := range metadata.Circuits {
					if entry.Spec == spec {
						check(t, ceremony.Check(filepath.Join(compiled, spec.ID+".r1cs"), entry.R1CS))
						found = true
						break
					}
				}
				if !found {
					t.Fatal("precompiled circuit missing")
				}
				ccs, e = ceremony.ReadR1CS(filepath.Join(compiled, spec.ID+".r1cs"))
			} else {
				ccs, e = compile(spec)
			}
			check(t, e)
			r1csPath := filepath.Join(root, spec.ID+".r1cs")
			check(t, ceremony.WriteBinary(r1csPath, ccs))
			dir := filepath.Join(root, "ceremony")
			t.Log("initializing Phase 2 (may take several minutes)")
			if _, e = os.Stat(dir); os.IsNotExist(e) {
				check(t, ceremony.Prepare(dir, revision, srsPath, info, []ceremony.Input{{Spec: spec, R1CS: r1csPath}}, 1<<21))
			}
			s, e := ceremony.Open(dir)
			check(t, e)
			if s.Manifest.SourceRevision != revision || len(s.Manifest.Circuits) != 1 || s.Manifest.Circuits[0].Spec != spec || s.Manifest.Phase1.Artifact != info.Artifact {
				t.Fatal("rehearsal checkpoint provenance mismatch")
			}
			server := httptest.NewServer(s.Handler())
			defer server.Close()
			for _, name := range []string{"alice", "bob"} {
				t.Log("contributing:", name)
				i, e := s.InviteQueued(server.URL, name, ceremony.Core, 24*time.Hour)
				check(t, e)
				c := ceremony.NewClient(filepath.Join(root, name))
				c.Progress = io.Discard
				check(t, c.Run(context.Background(), i, ceremony.Core))
			}
			out := filepath.Join(root, "export")
			t.Log("auditing transcript and exporting keys (replays initialization)")
			if _, e = os.Stat(out); os.IsNotExist(e) {
				check(t, s.Export(ceremony.Core, out, bytes.Repeat([]byte{0x42}, 32), 1<<21))
			}
			var exported ceremony.ExportManifest
			check(t, ceremony.ReadJSON(filepath.Join(out, "export.json"), &exported))
			if exported.ManifestSHA256 != s.ManifestArtifact.SHA256 || len(exported.Circuits) != 1 {
				t.Fatal("export provenance mismatch")
			}
			check(t, ceremony.Check(filepath.Join(out, spec.ID+".pk"), exported.Circuits[0].PK))
			check(t, ceremony.Check(filepath.Join(out, spec.ID+".vk"), exported.Circuits[0].VK))
			pk := groth16.NewProvingKey(ecc.BN254)
			vk := groth16.NewVerifyingKey(ecc.BN254)
			check(t, ceremony.ReadBinary(filepath.Join(out, spec.ID+".pk"), pk))
			check(t, ceremony.ReadBinary(filepath.Join(out, spec.ID+".vk"), vk))
			// Exercise the exact .key writer and Zolana's real file loader before
			// proving. These are explicitly marked rehearsal artifacts.
			keyPath := filepath.Join(root, spec.ID+".key")
			system, e := wrapSystem(spec, pk, vk, ccs)
			check(t, e)
			check(t, ceremony.WriteBinary(keyPath, system))
			loaded, e := common.ReadSystemFromFile(keyPath)
			check(t, e)
			ps, ok := loaded.(*common.TransferProofSystem)
			if !ok {
				t.Fatal("unexpected packaged proving system")
			}
			if ps.NInputs != spec.Inputs || ps.NOutputs != spec.Outputs || ps.HasCommitment != (spec.Family == "merge") {
				t.Fatal("packaged header mismatch")
			}
			pk, vk, ccs = ps.ProvingKey, ps.VerifyingKey, ps.ConstraintSystem
			full, e := witness.New(ecc.BN254.ScalarField())
			check(t, e)
			check(t, ceremony.ReadBinary(filepath.Join(fixtures, spec.ID+".key.0.witness"), full))
			public, e := full.Public()
			check(t, e)
			t.Log("proving and verifying valid Zolana witness")
			proof, e := groth16.Prove(ccs, pk, full)
			check(t, e)
			check(t, groth16.Verify(proof, vk, public))
			// Zolana's legacy HasCommitment file-header bit does not describe
			// the compiled circuit's BSB22 commitments.
			wantCommitments := len(ccs.GetCommitments().(constraint.Groth16Commitments))
			if len(proof.(*groth.Proof).Commitments) != wantCommitments {
				t.Fatal("proof commitment count differs from compiled circuit")
			}
			if vectors := os.Getenv("SPR_VECTORS"); vectors != "" {
				var vkRaw bytes.Buffer
				_, e = vk.(gnarkio.WriterRawTo).WriteRawTo(&vkRaw)
				check(t, e)
				p := proof.(*groth.Proof)
				var publicBytes [][32]byte
				for _, v := range public.Vector().(fr.Vector) {
					publicBytes = append(publicBytes, v.Bytes())
				}
				a := p.Ar.RawBytes()
				b := p.Bs.RawBytes()
				c := p.Krs.RawBytes()
				vector := struct {
					ID         string     `json:"id"`
					VK         []byte     `json:"vk"`
					A          []byte     `json:"a"`
					B          []byte     `json:"b"`
					C          []byte     `json:"c"`
					Public     [][32]byte `json:"public"`
					Commitment []byte     `json:"commitment,omitempty"`
					PoK        []byte     `json:"pok,omitempty"`
				}{ID: spec.ID, VK: vkRaw.Bytes(), A: a[:], B: b[:], C: c[:], Public: publicBytes}
				if len(p.Commitments) > 0 {
					x := p.Commitments[0].RawBytes()
					y := p.CommitmentPok.RawBytes()
					vector.Commitment = x[:]
					vector.PoK = y[:]
				}
				check(t, ceremony.WriteJSON(filepath.Join(vectors, spec.ID+".json"), vector))
			}
			check(t, ceremony.WriteJSON(filepath.Join(root, "result.json"), struct {
				Source         string  `json:"source_revision"`
				Engine         string  `json:"engine"`
				Circuit        string  `json:"circuit"`
				Constraints    int     `json:"constraints"`
				ElapsedSeconds float64 `json:"elapsed_seconds"`
				Passed         bool    `json:"passed"`
				Rehearsal      bool    `json:"insecure_test_setup"`
			}{revision, ceremony.Engine, spec.ID, ccs.GetNbConstraints(), time.Since(started).Seconds(), true, true}))
		})
	}
}
