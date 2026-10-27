package storage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	ceremony "github.com/helius-labs/spr-ceremony"
	"github.com/helius-labs/spr-ceremony/storage"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type failedCommit struct{ ceremony.BlobStore }

func (f failedCommit) ConditionalPut(context.Context, string, ceremony.Artifact, string) error {
	return errors.New("injected publication outage")
}

func TestS3AcceptanceRestoreAndForkProtection(t *testing.T) {
	endpoint := os.Getenv("SPR_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set SPR_S3_ENDPOINT to the local S3 emulator")
	}
	if !strings.HasPrefix(endpoint, "http://127.0.0.1:") && !strings.HasPrefix(endpoint, "http://localhost:") {
		t.Fatal("test endpoint must be loopback")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "testing")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "testing")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	ctx := context.Background()
	remote, err := storage.New(ctx, storage.Config{Bucket: "spr-ceremony-test", Region: "us-east-1", Prefix: fmt.Sprintf("test-%d", time.Now().UnixNano()), Endpoint: endpoint})
	must(t, err)
	_, err = remote.Client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(remote.Config.Bucket)})
	must(t, err)
	dir := filepath.Join(t.TempDir(), "ceremony")
	must(t, ceremony.Demo(dir))
	s, err := ceremony.Open(dir)
	must(t, err)
	s.Remote = remote
	initial, err := s.Sync(ctx)
	must(t, err)
	forkDir := filepath.Join(t.TempDir(), "fork")
	must(t, ceremony.Restore(ctx, remote, forkDir, s.ManifestArtifact, initial))
	fork, err := ceremony.Open(forkDir)
	must(t, err)
	fork.Remote = remote
	// A storage outage must leave the accepted head unchanged.
	i, err := s.Invite("http://localhost:8080", "failure-test", ceremony.Core, time.Hour)
	must(t, err)
	job := i.Jobs[0]
	output := filepath.Join(t.TempDir(), "output.ph2")
	must(t, ceremony.Contribute(s.Object(job.Input), output, job.Circuit.Shape))
	s.Remote = failedCommit{remote}
	if _, err = s.Accept(i.Token, job.Circuit.Spec.ID, output); err == nil {
		t.Fatal("accepted before remote publication")
	}
	state, err := s.State(job.Circuit.Spec.ID)
	must(t, err)
	if state.Head != job.Input {
		t.Fatal("failed publication advanced local head")
	}
	s.Remote = remote
	_, err = s.Accept(i.Token, job.Circuit.Spec.ID, output)
	must(t, err)
	_, err = s.Accept(i.Token, job.Circuit.Spec.ID, output)
	must(t, err)
	state, err = s.State(job.Circuit.Spec.ID)
	must(t, err)
	if len(state.History) != 1 {
		t.Fatal("retry duplicated contribution")
	}
	// A restored stale coordinator cannot overwrite the newer remote history.
	fi, err := fork.Invite("http://localhost:8080", "fork", ceremony.Core, time.Hour)
	must(t, err)
	forkOutput := filepath.Join(t.TempDir(), "fork.ph2")
	must(t, ceremony.Contribute(fork.Object(fi.Jobs[0].Input), forkOutput, fi.Jobs[0].Circuit.Shape))
	if _, err = fork.Accept(fi.Token, fi.Jobs[0].Circuit.Spec.ID, forkOutput); !errors.Is(err, ceremony.ErrRemoteConflict) {
		t.Fatalf("expected remote fork rejection: %v", err)
	}
	// The real participant client uses a presigned S3 download and coordinator
	// verification, then every accepted checkpoint is recoverable from S3.
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	invitation, err := s.InviteQueued(server.URL, "alice", ceremony.Core, time.Hour)
	must(t, err)
	client := ceremony.NewClient(t.TempDir())
	client.Progress = io.Discard
	must(t, client.Run(ctx, invitation, ceremony.Core))
	must(t, client.Run(ctx, invitation, ceremony.Core))
	checkpoint, err := s.Sync(ctx)
	must(t, err)
	restoredDir := filepath.Join(t.TempDir(), "restored")
	must(t, ceremony.Restore(ctx, remote, restoredDir, s.ManifestArtifact, checkpoint))
	restored, err := ceremony.Open(restoredDir)
	must(t, err)
	must(t, restored.Audit(ceremony.Core, 64))
	for _, c := range s.Manifest.Circuits {
		want, e := s.State(c.Spec.ID)
		must(t, e)
		got, e := restored.State(c.Spec.ID)
		must(t, e)
		if want.Head != got.Head || len(want.History) != len(got.History) {
			t.Fatal("restore lost accepted history")
		}
	}
	if _, err = os.Stat(filepath.Join(restoredDir, "invitations")); !os.IsNotExist(err) {
		t.Fatal("private invitations were restored from public storage")
	}
	objects, err := remote.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: &remote.Config.Bucket, Prefix: aws.String(remote.Config.Prefix + "/")})
	must(t, err)
	for _, obj := range objects.Contents {
		key := aws.ToString(obj.Key)
		if strings.Contains(key, "invitations") || strings.Contains(key, "leases") || strings.Contains(key, "s3.json") {
			t.Fatal("private coordinator data was uploaded")
		}
	}
	// A successful S3 write with a lost response is resolved by reading the
	// desired pointer; a retry must not create a divergent coordinator.
	pointerKey := "test-pointer.json"
	must(t, remote.ConditionalPut(ctx, pointerKey, initial, ""))
	_, etag, err := remote.ReadPointer(ctx, pointerKey)
	must(t, err)
	options := remote.Client.Options()
	dropped := &lostPutResponse{next: options.HTTPClient}
	options.HTTPClient = dropped
	options.RetryMaxAttempts = 1
	uncertain := &storage.S3{Client: s3.New(options), Config: remote.Config}
	must(t, uncertain.ConditionalPut(ctx, pointerKey, checkpoint, etag))
	if !dropped.dropped.Load() {
		t.Fatal("response loss was not injected")
	}
	actual, _, err := remote.ReadPointer(ctx, pointerKey)
	must(t, err)
	if actual != checkpoint {
		t.Fatal("lost response did not preserve successful commit")
	}
	// Even matching custom SHA metadata must not authenticate different bytes.
	corruptKey := remote.Config.Prefix + "/corrupt-test"
	original := []byte("original")
	local := filepath.Join(t.TempDir(), "object")
	must(t, os.WriteFile(local, original, 0600))
	artifact, err := ceremony.Digest(local)
	must(t, err)
	_, err = remote.Client.PutObject(ctx, &s3.PutObjectInput{Bucket: &remote.Config.Bucket, Key: &corruptKey,
		Body: bytes.NewReader([]byte("tampered")), Metadata: map[string]string{"sha256": artifact.SHA256}})
	must(t, err)
	if err = remote.PutObject(ctx, "corrupt-test", local, artifact); err == nil {
		t.Fatal("trusted corrupt remote bytes")
	}
	if err = remote.GetObject(ctx, "corrupt-test", filepath.Join(t.TempDir(), "download"), artifact); err == nil {
		t.Fatal("downloaded corrupt bytes")
	}
	// ETags are used for compare-and-swap only, never as content hashes.
	key := "campaigns/" + s.ManifestArtifact.SHA256 + "/latest.json"
	if err = remote.ConditionalPut(ctx, key, initial, "\"stale-etag\""); !errors.Is(err, ceremony.ErrRemoteConflict) {
		t.Fatalf("conditional write did not reject stale ETag: %v", err)
	}
}

// The server commits the PUT but the caller loses its response.
type lostPutResponse struct {
	next interface {
		Do(*http.Request) (*http.Response, error)
	}
	dropped atomic.Bool
}

func (c *lostPutResponse) Do(req *http.Request) (*http.Response, error) {
	res, err := c.next.Do(req)
	if err == nil && req.Method == "PUT" && res.StatusCode < 300 && c.dropped.CompareAndSwap(false, true) {
		res.Body.Close()
		return nil, errors.New("injected lost PUT response")
	}
	return res, err
}
