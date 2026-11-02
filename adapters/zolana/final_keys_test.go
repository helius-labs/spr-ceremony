package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark/backend/groth16"
	groth "github.com/consensys/gnark/backend/groth16/bn254"
	"github.com/consensys/gnark/backend/witness"
	gnarkio "github.com/consensys/gnark/io"
	ceremony "github.com/helius-labs/spr-ceremony"
	"github.com/helius-labs/zolana/prover/server/prover/common"
)

// Tests and packages the immutable November 2 release, whose transcript and
// final keys were independently verified before publication. This does not
// generate setup parameters or replace transcript verification. Exact export
// and manifest pins bind every input; freshly compiled R1CS must match too.
func TestFinalizedCoreIntegration(t *testing.T) {
	exported, fixtures, out := os.Getenv("SPR_FINAL_EXPORT"), os.Getenv("SPR_WITNESSES"), os.Getenv("SPR_PACKAGE")
	if exported == "" || fixtures == "" || out == "" {
		t.Skip("run scripts/test-final-keys.sh")
	}
	const manifest = "4b2b4465b79b5670d896e18dfb6fb86d02023033e9aeeb43e7a14911cb402079"
	const checkpoint = "5c525e4b1547516cf63678c4a235cb2fd05cd1f78a463a3faf44e2ef1cbc41d7"
	const exportHash = "ed506cb30158a722fcc9692d44960599e73a4028828b0e6dc6266ec597af8caa"
	ref, err := ceremony.Digest(filepath.Join(exported, "export.json"))
	check(t, err)
	if ref.SHA256 != exportHash {
		t.Fatal("final release export digest mismatch")
	}
	var m ceremony.ExportManifest
	check(t, ceremony.ReadJSON(filepath.Join(exported, "export.json"), &m))
	if m.ManifestSHA256 != manifest || m.InsecureTestSetup || len(m.Circuits) != 41 {
		t.Fatal("invalid final release")
	}
	if len(os.Getenv("SPR_SOURCE_REVISION")) != 40 {
		t.Fatal("missing integration source revision")
	}
	check(t, os.Mkdir(out, 0700))
	vectors := filepath.Join(out, "vectors")
	check(t, os.Mkdir(vectors, 0700))
	keys, rawVKs, compiled := map[string]ceremony.Artifact{}, map[string]ceremony.Artifact{}, map[string]ceremony.Artifact{}
	var mu sync.Mutex
	t.Run("circuits", func(t *testing.T) {
		for _, entry := range m.Circuits {
			t.Run(entry.Circuit.Spec.ID, func(t *testing.T) {
				t.Parallel()
				spec := entry.Circuit.Spec
				if !ceremony.IsCatalogSpec(spec) || spec.Track != ceremony.Core || entry.Contributions != 15 {
					t.Fatal("unexpected release circuit")
				}
				ccs, e := compile(spec)
				check(t, e)
				r1cs := filepath.Join(t.TempDir(), "circuit.r1cs")
				check(t, ceremony.WriteBinary(r1cs, ccs))
				check(t, ceremony.Check(r1cs, entry.Circuit.R1CS))
				pk, vk := groth16.NewProvingKey(ecc.BN254), groth16.NewVerifyingKey(ecc.BN254)
				for ext, ref := range map[string]ceremony.Artifact{"pk": entry.PK, "vk": entry.VK} {
					check(t, ceremony.Check(filepath.Join(exported, spec.ID+"."+ext), ref))
				}
				check(t, ceremony.ReadBinary(filepath.Join(exported, spec.ID+".pk"), pk))
				check(t, ceremony.ReadBinary(filepath.Join(exported, spec.ID+".vk"), vk))
				system, e := wrapSystem(spec, pk, vk, ccs)
				check(t, e)
				keyName, vkName := spec.ID+".key", spec.ID+".vkbin"
				check(t, ceremony.WriteBinary(filepath.Join(out, keyName), system))
				loaded, e := common.ReadSystemFromFile(filepath.Join(out, keyName))
				check(t, e)
				ps, ok := loaded.(*common.TransferProofSystem)
				if !ok {
					t.Fatal("unexpected proving system")
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
				proof, e := groth16.Prove(ccs, pk, full)
				check(t, e)
				check(t, groth16.Verify(proof, vk, public))
				var raw bytes.Buffer
				_, e = vk.(gnarkio.WriterRawTo).WriteRawTo(&raw)
				check(t, e)
				check(t, os.WriteFile(filepath.Join(out, vkName), raw.Bytes(), 0600))
				{

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
				altered, e := full.Public()
				check(t, e)
				inputs := altered.Vector().(fr.Vector)
				inputs[0].Add(&inputs[0], new(fr.Element).SetOne())
				if groth16.Verify(proof, vk, altered) == nil {
					t.Fatal("changed public input accepted")
				}
				keyRef, e := ceremony.Digest(filepath.Join(out, keyName))
				check(t, e)
				vkRef, e := ceremony.Digest(filepath.Join(out, vkName))
				check(t, e)
				mu.Lock()
				keys[keyName] = keyRef
				rawVKs[vkName] = vkRef
				compiled[spec.ID] = entry.Circuit.R1CS
				mu.Unlock()
			})
		}
	})
	if t.Failed() {
		return
	}
	if len(keys) != 41 {
		t.Fatal("incomplete package")
	}
	metadata := map[string]interface{}{
		"source_revision":             "ff04c625ceb43c688cd048549e00a5dabc0c6ee6",
		"integration_source_revision": os.Getenv("SPR_SOURCE_REVISION"),
		"manifest":                    ceremony.Artifact{SHA256: manifest, Size: 31586},
		"checkpoint":                  ceremony.Artifact{SHA256: checkpoint, Size: 17855},
		"export":                      ref, "keys": keys, "raw_vks": rawVKs, "compiled_r1cs": compiled,
	}
	check(t, ceremony.WriteJSON(filepath.Join(out, "package.json"), metadata))
	b, e := os.ReadFile(filepath.Join(exported, "export.json"))
	check(t, e)
	check(t, os.WriteFile(filepath.Join(out, "ceremony-export.json"), b, 0600))
}
