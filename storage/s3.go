// Package storage implements the public-artifact store. Credentials are resolved
// only on the coordinator using the standard AWS credential chain.
package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	ceremony "github.com/helius-labs/spr-ceremony"
)

var safePrefix = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/_-]*$`)

type Config struct {
	Bucket   string `json:"bucket"`
	Region   string `json:"region"`
	Prefix   string `json:"prefix"`
	Endpoint string `json:"endpoint,omitempty"`
}
type S3 struct {
	Client *s3.Client
	Config Config
}

func New(ctx context.Context, c Config) (*S3, error) {
	if c.Bucket == "" || c.Region == "" || !safePrefix.MatchString(c.Prefix) || strings.HasSuffix(c.Prefix, "/") {
		return nil, fmt.Errorf("S3 bucket, region, and a relative campaign prefix are required")
	}
	if c.Endpoint != "" {
		if err := ceremony.ValidateURL(c.Endpoint); err != nil {
			return nil, err
		}
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(c.Region), config.WithHTTPClient(&http.Client{Timeout: 30 * time.Minute}))
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
			o.UsePathStyle = true
		}
	})
	return &S3{Client: client, Config: c}, nil
}
func (s *S3) key(key string) string { return strings.TrimRight(s.Config.Prefix, "/") + "/" + key }
func checksum(a ceremony.Artifact) string {
	b, _ := hex.DecodeString(a.SHA256)
	return base64.StdEncoding.EncodeToString(b)
}
func code(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) {
		return api.ErrorCode()
	}
	return ""
}

func (s *S3) PutObject(ctx context.Context, key, path string, a ceremony.Artifact) error {
	if err := a.Validate(); err != nil {
		return err
	}
	// Core artifacts are bounded below the S3 single-PUT limit. Do not silently
	// apply this transport to an unbenchmarked larger circuit.
	if a.Size > 5<<30 {
		return fmt.Errorf("artifact exceeds the core S3 transport's 5 GiB limit")
	}
	head, err := s.Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.Config.Bucket, Key: aws.String(s.key(key)), ChecksumMode: types.ChecksumModeEnabled})
	if err == nil {
		return s.verifyExisting(ctx, key, a, head)
	}
	if code(err) != "NotFound" && code(err) != "NoSuchKey" && code(err) != "404" {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = s.Client.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.Config.Bucket, Key: aws.String(s.key(key)), Body: f, ContentLength: &a.Size, IfNoneMatch: aws.String("*"), ChecksumSHA256: aws.String(checksum(a)), Metadata: map[string]string{"sha256": a.SHA256}})
	if code(err) == "PreconditionFailed" || code(err) == "ConditionalRequestConflict" {
		// Another writer can only win an immutable key with the same bytes.
		h, e := s.Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.Config.Bucket, Key: aws.String(s.key(key)), ChecksumMode: types.ChecksumModeEnabled})
		if e == nil {
			return s.verifyExisting(ctx, key, a, h)
		}
		return fmt.Errorf("immutable S3 object conflict: %s", key)
	}
	return err
}

func (s *S3) verifyExisting(ctx context.Context, key string, a ceremony.Artifact, head *s3.HeadObjectOutput) error {
	if aws.ToInt64(head.ContentLength) != a.Size {
		return fmt.Errorf("remote object size mismatch: %s", key)
	}
	if head.ChecksumSHA256 != nil {
		if *head.ChecksumSHA256 != checksum(a) {
			return fmt.Errorf("remote object checksum mismatch: %s", key)
		}
		return nil
	}
	// Some S3-compatible implementations do not expose stored checksums.
	// Verify their actual bytes instead of trusting user-supplied metadata.
	r, err := s.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.Config.Bucket, Key: aws.String(s.key(key))})
	if err != nil {
		return err
	}
	defer r.Body.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r.Body, a.Size+1))
	if err != nil {
		return err
	}
	if n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return fmt.Errorf("remote object content mismatch: %s", key)
	}
	return nil
}

func (s *S3) GetObject(ctx context.Context, key, path string, a ceremony.Artifact) error {
	if err := a.Validate(); err != nil {
		return err
	}
	r, err := s.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.Config.Bucket, Key: aws.String(s.key(key))})
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if aws.ToInt64(r.ContentLength) != a.Size {
		return fmt.Errorf("S3 object size mismatch")
	}
	if err = ceremony.Atomic(path, func(w io.Writer) error {
		n, e := io.Copy(w, io.LimitReader(r.Body, a.Size+1))
		if e != nil {
			return e
		}
		if n != a.Size {
			return fmt.Errorf("truncated or oversized S3 object")
		}
		return nil
	}); err != nil {
		return err
	}
	if err = ceremony.Check(path, a); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func (s *S3) ReadPointer(ctx context.Context, key string) (ceremony.Artifact, string, error) {
	var a ceremony.Artifact
	r, err := s.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.Config.Bucket, Key: aws.String(s.key(key))})
	if code(err) == "NoSuchKey" || code(err) == "NotFound" {
		return a, "", ceremony.ErrRemoteMissing
	}
	if err != nil {
		return a, "", err
	}
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil {
		return a, "", err
	}
	if len(b) > 4096 {
		return a, "", fmt.Errorf("oversized checkpoint pointer")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&a); err != nil {
		return a, "", err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return a, "", fmt.Errorf("invalid checkpoint pointer")
	}
	if err = a.Validate(); err != nil {
		return a, "", err
	}
	if aws.ToString(r.ETag) == "" {
		return a, "", fmt.Errorf("S3 checkpoint has no ETag")
	}
	return a, aws.ToString(r.ETag), nil
}

func (s *S3) ConditionalPut(ctx context.Context, key string, a ceremony.Artifact, etag string) error {
	if err := a.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	in := &s3.PutObjectInput{Bucket: &s.Config.Bucket, Key: aws.String(s.key(key)), Body: bytes.NewReader(b), ContentType: aws.String("application/json")}
	if etag == "" {
		in.IfNoneMatch = aws.String("*")
	} else {
		in.IfMatch = &etag
	}
	_, err = s.Client.PutObject(ctx, in)
	if err != nil {
		// A lost PUT response may hide a successful commit. Resolve by reading
		// the exact pointer before reporting a conflict or retryable failure.
		current, _, readErr := s.ReadPointer(ctx, key)
		if readErr == nil && current == a {
			return nil
		}
		if code(err) == "PreconditionFailed" || code(err) == "ConditionalRequestConflict" {
			return ceremony.ErrRemoteConflict
		}
	}
	return err
}

func (s *S3) DownloadURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > time.Hour {
		return "", fmt.Errorf("download URL lifetime must be at most one hour")
	}
	r, err := s3.NewPresignClient(s.Client).PresignGetObject(ctx, &s3.GetObjectInput{Bucket: &s.Config.Bucket, Key: aws.String(s.key(key))}, func(o *s3.PresignOptions) { o.Expires = ttl })
	if err != nil {
		return "", err
	}
	return r.URL, nil
}
