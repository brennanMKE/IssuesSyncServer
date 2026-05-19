package storage

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// PutObject uploads body to S3 under the given key, storing the SHA-256 ETag as
// object metadata so that an out-of-band restore from S3 can rebuild the index.
func PutObject(ctx context.Context, client *s3.Client, bucket, key string, body []byte, contentType, etag string) error {
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
		Metadata: map[string]string{
			"etag-sha256": etag,
		},
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
	})
	if err != nil {
		return fmt.Errorf("s3: put object %q: %w", key, err)
	}
	return nil
}

// DeleteS3Object deletes an object from S3. Errors are logged but not returned —
// the caller has already committed the Postgres mutation and should not fail the
// request because of a best-effort S3 cleanup.
func DeleteS3Object(ctx context.Context, client *s3.Client, bucket, key string) error {
	_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		slog.Error("s3: delete object failed (best-effort)", "key", key, "err", err)
	}
	return err
}
