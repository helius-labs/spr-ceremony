package ceremony

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ContributionRecord reports acceptance, not an independent MPC audit or a
// participant identity signature. It contains no invitation bearer credentials.
type ContributionRecord struct {
	Format         string    `json:"format"`
	Status         string    `json:"status"`
	Manifest       Artifact  `json:"manifest"`
	Checkpoint     Artifact  `json:"checkpoint"`
	CoordinatorKey Artifact  `json:"coordinator_key"`
	InvitationID   string    `json:"invitation_id"`
	Contributor    string    `json:"contributor"`
	Track          string    `json:"track"`
	Total          int       `json:"total"`
	Receipts       []Receipt `json:"receipts"`
}

type ContributionRecordSummary struct {
	InvitationID string `json:"invitation_id"`
	Contributor  string `json:"contributor"`
	Status       string `json:"status"`
	Total        int    `json:"total"`
}

// Cache signature verification by immutable checkpoint. This lock is local to
// public reads and never shares the coordinator's acceptance/publication lock.
func (s *Store) contributionRecordHandler() http.HandlerFunc {
	var mutex sync.Mutex
	var cached Artifact
	var records []ContributionRecord
	load := func() ([]ContributionRecord, Artifact, error) {
		mutex.Lock()
		defer mutex.Unlock()
		_, ref, err := s.checkpoint()
		if err != nil {
			return nil, ref, err
		}
		if ref != cached {
			view, err := s.Snapshot(ref)
			if err != nil {
				return nil, ref, err
			}
			next, err := view.ContributionRecords()
			if err != nil {
				return nil, ref, err
			}
			records, cached = next, ref
		}
		return records, ref, nil
	}
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id != "" && !validInvitationID(id) {
			http.NotFound(w, r)
			return
		}
		records, ref, err := load()
		if err != nil {
			http.Error(w, "Contribution records unavailable", http.StatusServiceUnavailable)
			return
		}
		if id != "" {
			for _, record := range records {
				if record.InvitationID == id {
					respond(w, record)
					return
				}
			}
			http.NotFound(w, r)
			return
		}
		items := []ContributionRecordSummary{}
		for _, record := range records {
			items = append(items, ContributionRecordSummary{record.InvitationID, record.Contributor, record.Status, record.Total})
		}
		respond(w, struct {
			Manifest   Artifact                    `json:"manifest"`
			Checkpoint Artifact                    `json:"checkpoint"`
			Records    []ContributionRecordSummary `json:"records"`
		}{s.ManifestArtifact, ref, items})
	}
}

// ContributionRecords uses one checkpoint and publishes only complete core
// participation. Reading it never changes acceptance, invitations or S3 state.
func (s *Store) ContributionRecords() ([]ContributionRecord, error) {
	view, err := s.snapshot()
	if err != nil {
		return nil, err
	}
	if view.Manifest.CoordinatorKey == nil {
		return []ContributionRecord{}, nil
	}
	if _, err = view.PublicSigningKey(); err != nil {
		return nil, err
	}
	groups := map[string]*ContributionRecord{}
	total := 0
	for _, c := range view.Manifest.Circuits {
		if c.Spec.Track != Core {
			continue
		}
		total++
		state, err := view.State(c.Spec.ID)
		if err != nil {
			return nil, err
		}
		parent, seen := c.Initial, map[string]bool{}
		for n, receipt := range state.History {
			if receipt.Circuit != c.Spec.ID || receipt.Sequence != n+1 || receipt.Parent != parent || receipt.Output.Validate() != nil || !validInvitationID(receipt.InvitationID) || !safeID.MatchString(receipt.Contributor) || seen[receipt.InvitationID] {
				return nil, fmt.Errorf("invalid contribution record history")
			}
			if err = view.verifyMessage("receipt", receipt); err != nil {
				return nil, err
			}
			seen[receipt.InvitationID], parent = true, receipt.Output
			record := groups[receipt.InvitationID]
			if record == nil {
				record = &ContributionRecord{Format: "spr-contribution-record-v1", Status: "accepted", Manifest: view.ManifestArtifact, Checkpoint: *view.checkpointRef, CoordinatorKey: *view.Manifest.CoordinatorKey, InvitationID: receipt.InvitationID, Contributor: receipt.Contributor, Track: Core}
				groups[receipt.InvitationID] = record
			}
			if record.Contributor != receipt.Contributor {
				return nil, fmt.Errorf("contribution identity mismatch")
			}
			record.Receipts = append(record.Receipts, receipt)
		}
		if state.Circuit != c.Spec.ID || state.Head != parent {
			return nil, fmt.Errorf("contribution head mismatch")
		}
	}
	records := []ContributionRecord{}
	for _, record := range groups {
		if total > 0 && len(record.Receipts) == total {
			record.Total = total
			sort.Slice(record.Receipts, func(i, j int) bool { return record.Receipts[i].Circuit < record.Receipts[j].Circuit })
			records = append(records, *record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].InvitationID < records[j].InvitationID })
	return records, nil
}

// FetchContributionRecords needs only public metadata, not AWS credentials,
// signing keys or the large proving artifacts. The manifest pin is supplied
// independently; checkpoint inclusion and receipt signatures are checked here.
// Full transcript initialization/contribution verification remains a separate audit.
func FetchContributionRecords(ctx context.Context, origin, manifestSHA string) ([]ContributionRecord, error) {
	if err := ValidateURL(origin); err != nil {
		return nil, err
	}
	if (Artifact{SHA256: manifestSHA, Size: 1}).Validate() != nil {
		return nil, fmt.Errorf("authenticated manifest SHA256 required")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	o := &Operator{Config: OperatorConfig{URL: strings.TrimRight(origin, "/")}, client: NewClient("")}
	req, err := http.NewRequestWithContext(ctx, "GET", o.Config.URL+"/v1/ceremony", nil)
	if err != nil {
		return nil, err
	}
	response, err := o.client.HTTP.Do(req)
	if err != nil {
		return nil, safeRequestError(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxJSONBytes+1))
	if err != nil || response.StatusCode != http.StatusOK || len(body) > maxJSONBytes {
		return nil, fmt.Errorf("public ceremony unavailable")
	}
	var public PublicCeremony
	if err = decodeRemoteJSON(bytes.NewReader(body), &public); err != nil {
		return nil, err
	}
	if public.Format != "spr-public-ceremony-v1" || public.Manifest.SHA256 != manifestSHA {
		return nil, fmt.Errorf("ceremony differs from authenticated manifest")
	}
	o.Config.Manifest = public.Manifest
	manifest, manifestBytes, err := o.manifest(ctx)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "spr-public-records-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	store := &Store{Dir: dir, Manifest: manifest, ManifestArtifact: public.Manifest, checkpointRef: &public.Checkpoint}
	if err = os.Mkdir(filepath.Join(dir, "objects"), 0700); err != nil {
		return nil, err
	}
	fetch := func(ref Artifact) error {
		data, err := o.publicObject(ctx, ref, maxJSONBytes)
		if err != nil {
			return err
		}
		return os.WriteFile(store.Object(ref), data, 0600)
	}
	if err = os.WriteFile(filepath.Join(dir, "manifest.json"), manifestBytes, 0600); err != nil {
		return nil, err
	}
	if err = fetch(public.Checkpoint); err != nil {
		return nil, err
	}
	cp, _, err := store.checkpoint()
	if err != nil {
		return nil, err
	}
	if err = fetch(*manifest.CoordinatorKey); err != nil {
		return nil, err
	}
	for _, ref := range cp.States {
		if err = fetch(ref); err != nil {
			return nil, err
		}
	}
	return store.ContributionRecords()
}

// SaveContributionRecords is append-only. A later checkpoint can witness the
// same receipts without rewriting the first archived record. Conflicts fail.
func SaveContributionRecords(dir string, records []ContributionRecord) error {
	if dir == "" {
		return fmt.Errorf("record output directory required")
	}
	for _, record := range records {
		if record.Manifest.Validate() != nil || !validInvitationID(record.InvitationID) || record.Format != "spr-contribution-record-v1" || record.Status != "accepted" || record.Total < 1 || record.Total > 41 || len(record.Receipts) != record.Total {
			return fmt.Errorf("invalid complete contribution record")
		}
		parent := filepath.Join(dir, record.Manifest.SHA256)
		if err := os.MkdirAll(parent, 0755); err != nil {
			return err
		}
		path := filepath.Join(parent, record.InvitationID+".json")
		var previous ContributionRecord
		if err := ReadJSON(path, &previous); err == nil {
			if previous.Checkpoint.Validate() != nil {
				return fmt.Errorf("invalid archived checkpoint reference")
			}
			// Checkpoint is the first archived witness, not necessarily today's head.
			previous.Checkpoint = record.Checkpoint
			a, _ := json.Marshal(previous)
			b, _ := json.Marshal(record)
			if !bytes.Equal(a, b) {
				return fmt.Errorf("archived contribution record conflicts with published receipts")
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(record)
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			_ = os.Remove(path)
			return err
		}
		if closeErr != nil {
			_ = os.Remove(path)
			return closeErr
		}
	}
	return nil
}
