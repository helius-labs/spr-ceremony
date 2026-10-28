package ceremony

import (
	"fmt"
	"os"
	"path/filepath"
)

func sameReceipt(a, b Receipt) bool {
	return a.InvitationID == b.InvitationID && a.Signature == b.Signature && a.Circuit == b.Circuit && a.Sequence == b.Sequence && a.Contributor == b.Contributor &&
		a.Parent == b.Parent && a.Output == b.Output && a.AcceptedAt.Equal(b.AcceptedAt)
}

// recoverReceipts repairs the crash window between remote acceptance and saving
// the local receipt. A receipt alone never supplies evidence of a local contribution.
func (c *Client) recoverReceipts(i Invitation, receipts []Receipt) error {
	jobs := map[string]Job{}
	for _, j := range i.Jobs {
		jobs[j.Circuit.Spec.ID] = j
	}
	covered, seen := map[string]bool{}, map[string]bool{}
	for _, r := range receipts {
		job, ok := jobs[r.Circuit]
		key := fmt.Sprintf("%s/%d", r.Circuit, r.Sequence)
		if !ok || r.Contributor != i.Contributor || r.InvitationID != i.ID || r.Sequence < 1 || seen[key] ||
			r.Parent.Validate() != nil || r.Output.Validate() != nil ||
			r.Parent.Size > c.MaxFileBytes || r.Output.Size > c.MaxFileBytes+1024 {
			return fmt.Errorf("invalid completion receipt")
		}
		if err := c.verifyReceipt(i, r); err != nil {
			return err
		}
		covered[r.Circuit], seen[key] = true, true
		dir := filepath.Join(c.Work, i.ManifestSHA256, i.ID, r.Circuit, r.Parent.SHA256)
		path := filepath.Join(dir, "receipt.json")
		var saved Receipt
		if err := ReadJSON(path, &saved); err == nil {
			if !sameReceipt(saved, r) {
				return fmt.Errorf("saved receipt differs from coordinator: %s", r.Circuit)
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		input, output := filepath.Join(dir, "input.ph2"), filepath.Join(dir, "output.ph2")
		// Accepted work from another machine has no local files to attest to.
		missing := false
		for _, p := range []string{input, output} {
			if _, err := os.Stat(p); os.IsNotExist(err) {
				missing = true
			} else if err != nil {
				return err
			}
		}
		if missing {
			continue
		}
		if err := Check(input, r.Parent); err != nil {
			return err
		}
		if err := Check(output, r.Output); err != nil {
			return err
		}
		if err := Verify(input, output, job.Circuit.Shape); err != nil {
			return err
		}
		if err := WriteJSON(path, r); err != nil {
			return err
		}
	}
	if len(covered) != len(jobs) {
		return fmt.Errorf("completion response omitted an invited circuit")
	}
	return nil
}
