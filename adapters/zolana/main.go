// spr-zolana is built against an explicit Zolana checkout. The participant
// binary has no dependency on the private Zolana repository.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark/backend/groth16"
	"github.com/consensys/gnark/constraint"
	ceremony "github.com/helius-labs/spr-ceremony"
	"github.com/helius-labs/zolana/prover/server/prover/common"
	merge "github.com/helius-labs/zolana/prover/server/prover/merge"
	nullifier "github.com/helius-labs/zolana/prover/server/prover/nullifier_tree"
	transfer "github.com/helius-labs/zolana/prover/server/prover/transfer_eddsa"
)

var zolanaRevision = "unknown"
var zolanaDirty = "true"

func compile(spec ceremony.Spec) (constraint.ConstraintSystem, error) {
	switch spec.Family {
	case "transfer-confidential":
		return transfer.R1CSTransfer(spec.Inputs, spec.Outputs, transfer.ConfidentialVariant)
	case "merge":
		return merge.R1CSMerge(int(spec.Inputs))
	case "address-append":
		return nullifier.R1CSBatchAddressAppend(spec.Height, spec.Batch)
	default:
		return nil, fmt.Errorf("unsupported circuit family")
	}
}
func main() {
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, "error:", e)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: spr-zolana <compile|package> [flags]")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	track := f.String("track", ceremony.Core, "core or batch-4999")
	id := f.String("circuit", "", "single circuit ID")
	out := f.String("out", "", "new output directory")
	exported := f.String("export", "", "directory containing exported ceremony pk/vk files")
	storeDir := f.String("ceremony", "", "ceremony directory containing pinned R1CS objects")
	maxDomain := f.Uint64("max-domain", 1<<22, "maximum in-memory SRS domain during export verification")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if f.NArg() != 0 || *out == "" {
		return fmt.Errorf("--out is required; positional arguments are not accepted")
	}
	if zolanaRevision == "unknown" || zolanaDirty != "false" {
		return fmt.Errorf("build against a clean pinned checkout using scripts/build-zolana.sh")
	}
	if _, e := os.Stat(*out); !os.IsNotExist(e) {
		return fmt.Errorf("output directory already exists or is inaccessible")
	}
	if e := os.MkdirAll(filepath.Dir(*out), 0700); e != nil {
		return e
	}
	tmp, e := os.MkdirTemp(filepath.Dir(*out), ".zolana-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	switch args[0] {
	case "compile":
		specs, e := ceremony.Select(*track, *id)
		if e != nil {
			return e
		}
		available := map[string]bool{}
		for _, name := range common.KeyFiles() {
			available[name] = true
		}
		metadata := ceremony.Compilation{Engine: ceremony.Engine, SourceRevision: zolanaRevision}
		for _, spec := range specs {
			if !available[spec.ID+".key"] {
				return fmt.Errorf("catalog circuit %s is not supported by this checkout", spec.ID)
			}
			fmt.Fprintln(os.Stderr, "Compiling", spec.ID)
			ccs, e := compile(spec)
			if e != nil {
				return e
			}
			path := filepath.Join(tmp, spec.ID+".r1cs")
			if e = ceremony.WriteBinary(path, ccs); e != nil {
				return e
			}
			a, e := ceremony.Digest(path)
			if e != nil {
				return e
			}
			metadata.Circuits = append(metadata.Circuits, ceremony.CompiledCircuit{Spec: spec, R1CS: a, Constraints: ccs.GetNbConstraints(), Domain: ecc.NextPowerOfTwo(uint64(ccs.GetNbConstraints()))})
		}
		if e = ceremony.WriteJSON(filepath.Join(tmp, "compilation.json"), metadata); e != nil {
			return e
		}
	case "package":
		var exportedManifest ceremony.ExportManifest
		if e = ceremony.ReadJSON(filepath.Join(*exported, "export.json"), &exportedManifest); e != nil {
			return e
		}
		if exportedManifest.InsecureTestSetup {
			return fmt.Errorf("refusing to package rehearsal keys as Zolana proving systems")
		}
		if exportedManifest.Engine != ceremony.Engine || exportedManifest.Format != ceremony.Format || exportedManifest.Track != *track || len(exportedManifest.Circuits) == 0 {
			return fmt.Errorf("invalid key export")
		}
		s, e := ceremony.Open(*storeDir)
		if e != nil {
			return e
		}
		if s.ManifestArtifact.SHA256 != exportedManifest.ManifestSHA256 || s.Manifest.SourceRevision != zolanaRevision {
			return fmt.Errorf("ceremony source or manifest mismatch")
		}
		if e = s.VerifyExport(*exported, *maxDomain); e != nil {
			return e
		}
		for _, entry := range exportedManifest.Circuits {
			spec := entry.Circuit.Spec
			if !ceremony.IsCatalogSpec(spec) || spec.Track != *track {
				return fmt.Errorf("exported circuit is outside ceremony scope")
			}
			pkPath := filepath.Join(*exported, spec.ID+".pk")
			vkPath := filepath.Join(*exported, spec.ID+".vk")
			if e = ceremony.Check(pkPath, entry.PK); e != nil {
				return e
			}
			if e = ceremony.Check(vkPath, entry.VK); e != nil {
				return e
			}
			if e = ceremony.Check(s.Object(entry.Circuit.R1CS), entry.Circuit.R1CS); e != nil {
				return e
			}
			ccs, e := ceremony.ReadR1CS(s.Object(entry.Circuit.R1CS))
			if e != nil {
				return e
			}
			pk := groth16.NewProvingKey(ecc.BN254)
			vk := groth16.NewVerifyingKey(ecc.BN254)
			if e = ceremony.ReadBinary(pkPath, pk); e != nil {
				return e
			}
			if e = ceremony.ReadBinary(vkPath, vk); e != nil {
				return e
			}
			var system io.WriterTo
			switch spec.Family {
			case "transfer-confidential":
				system = &common.TransferProofSystem{CircuitType: common.TransferConfidentialCircuitType, NInputs: spec.Inputs, NOutputs: spec.Outputs, Confidential: true, ProvingKey: pk, VerifyingKey: vk, ConstraintSystem: ccs}
			case "merge":
				system = &common.TransferProofSystem{CircuitType: common.MergeCircuitType, NInputs: spec.Inputs, NOutputs: 1, HasCommitment: true, ProvingKey: pk, VerifyingKey: vk, ConstraintSystem: ccs}
			case "address-append":
				system = &common.BatchProofSystem{CircuitType: common.BatchAddressAppendCircuitType, TreeHeight: spec.Height, BatchSize: spec.Batch, ProvingKey: pk, VerifyingKey: vk, ConstraintSystem: ccs}
			default:
				return fmt.Errorf("unsupported circuit")
			}
			if e = ceremony.WriteBinary(filepath.Join(tmp, spec.ID+".key"), system); e != nil {
				return e
			}
		}
		if e = ceremony.WriteJSON(filepath.Join(tmp, "ceremony-export.json"), exportedManifest); e != nil {
			return e
		}
	default:
		return fmt.Errorf("unknown command")
	}
	return os.Rename(tmp, *out)
}
