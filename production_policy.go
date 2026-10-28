package ceremony

import "fmt"

// QuicknetChainHash is published by the League of Entropy at
// https://docs.drand.love/developer/API-v1/drand-http-api/.
// Production cannot replace this trust root with a caller-supplied chain.
const QuicknetChainHash = "52db9ba70e0cc0f6eaf7803dd07447a1f5477735fd3f661792ba94600c84e971"

// validatePhase1Policy is shared by preparation, opening, audit and export
// verification. A consistent caller-supplied hash is insufficient for production.
func validatePhase1Policy(info SRSInfo) error {
	if info.Format != Format || info.Engine != Engine || info.Provenance == "" || info.Artifact.Validate() != nil {
		return fmt.Errorf("invalid Phase 1 metadata")
	}
	if !info.InsecureTestSetup && info.Artifact != publicPower21().SRS {
		return fmt.Errorf("production requires the pinned public power-21 SRS")
	}
	return nil
}

func validateBeaconPolicy(rehearsal bool, plan *BeaconPlan) error {
	if !rehearsal && (plan == nil || plan.ChainHash != QuicknetChainHash) {
		return fmt.Errorf("production requires the pinned quicknet chain")
	}
	return nil
}
