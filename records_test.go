package ceremony

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestContributionRecordsCompletionRecoveryAndArchive(t *testing.T) {
	s, operator, origin := operatorFixture(t)
	ctx := context.Background()
	invite, err := operator.Invite(ctx, "alice", 24)
	must(t, err)
	claim, err := s.Claim(invite.Token, time.Minute)
	must(t, err)
	output := filepath.Join(t.TempDir(), "output")
	must(t, Contribute(s.Object(claim.Job.Input), output, claim.Job.Circuit.Shape))
	_, err = s.Accept(invite.Token, claim.Job.Circuit.Spec.ID, output)
	must(t, err)
	pinned, err := s.snapshot()
	must(t, err)
	records, err := s.ContributionRecords()
	must(t, err)
	if len(records) != 0 {
		t.Fatal("published incomplete contributor")
	}
	must(t, NewClient(t.TempDir()).Run(ctx, invite, Core))
	records, err = FetchContributionRecords(ctx, origin, s.ManifestArtifact.SHA256)
	must(t, err)
	if len(records) != 1 || records[0].Total != 2 || records[0].Status != "accepted" {
		t.Fatal("missing complete acceptance record")
	}
	old, err := pinned.ContributionRecords()
	must(t, err)
	if len(old) != 0 {
		t.Fatal("record mixed checkpoints")
	}
	if _, err = FetchContributionRecords(ctx, origin, strings.Repeat("0", 64)); err == nil {
		t.Fatal("accepted wrong manifest pin")
	}
	response, err := http.Get(origin + "/v1/records/" + invite.ID)
	must(t, err)
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	must(t, err)
	if response.StatusCode != 200 || strings.Contains(string(body), invite.Token) || strings.Contains(string(body), `"token"`) || strings.Contains(string(body), "audited") {
		t.Fatal("public record missing or leaks private state")
	}
	var hosted ContributionRecord
	must(t, json.Unmarshal(body, &hosted))
	if hosted.Checkpoint != records[0].Checkpoint || hosted.InvitationID != invite.ID {
		t.Fatal("hosted record differs from pinned public transcript")
	}
	out := t.TempDir()
	must(t, SaveContributionRecords(out, records))
	path := filepath.Join(out, s.ManifestArtifact.SHA256, invite.ID+".json")
	first, err := os.ReadFile(path)
	must(t, err)
	must(t, operator.Revoke(ctx, invite.ID))
	bob, err := operator.Invite(ctx, "alice", 24)
	must(t, err)
	must(t, NewClient(t.TempDir()).Run(ctx, bob, Core))
	records, err = FetchContributionRecords(ctx, origin, s.ManifestArtifact.SHA256)
	must(t, err)
	if len(records) != 2 {
		t.Fatal("revocation lost published acceptance")
	}
	must(t, SaveContributionRecords(out, records))
	after, err := os.ReadFile(path)
	must(t, err)
	if string(first) != string(after) {
		t.Fatal("later checkpoint rewrote first archived witness")
	}
	records[0].Receipts[0].Contributor = "changed"
	if SaveContributionRecords(out, records) == nil {
		t.Fatal("overwrote conflicting archived receipt")
	}
}

func TestContributionRecordsRejectForgedReceipt(t *testing.T) {
	s, operator, _ := operatorFixture(t)
	invite, err := operator.Invite(context.Background(), "alice", 24)
	must(t, err)
	must(t, NewClient(t.TempDir()).Run(context.Background(), invite, Core))
	cp, ref, err := s.checkpoint()
	must(t, err)
	circuit := s.Manifest.Circuits[0].Spec.ID
	state, err := s.State(circuit)
	must(t, err)
	state.History[0].Contributor = "forged"
	changed, err := s.putJSON(state)
	must(t, err)
	cp.States[circuit] = changed
	must(t, s.commitCheckpoint(cp, ref))
	if _, err = s.ContributionRecords(); err == nil {
		t.Fatal("published invalid signature")
	}
}
