package storage

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// CheckBucket verifies the storage prerequisites before coordinating a real
// ceremony. Rehearsals can use an isolated local S3-compatible server.
func (s *S3) CheckBucket(ctx context.Context) error {
	v, err := s.Client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: &s.Config.Bucket})
	if err != nil {
		return err
	}
	if v.Status != types.BucketVersioningStatusEnabled {
		return fmt.Errorf("ceremony bucket versioning must be enabled")
	}
	p, err := s.Client.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: &s.Config.Bucket})
	if err != nil {
		return err
	}
	b := p.PublicAccessBlockConfiguration
	if b == nil || !aws.ToBool(b.BlockPublicAcls) || !aws.ToBool(b.BlockPublicPolicy) || !aws.ToBool(b.IgnorePublicAcls) || !aws.ToBool(b.RestrictPublicBuckets) {
		return fmt.Errorf("ceremony bucket must block public access")
	}
	e, err := s.Client.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: &s.Config.Bucket})
	if err != nil {
		return err
	}
	if e.ServerSideEncryptionConfiguration == nil || len(e.ServerSideEncryptionConfiguration.Rules) == 0 {
		return fmt.Errorf("ceremony bucket must enable server-side encryption")
	}
	return nil
}
