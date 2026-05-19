package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// GetObject fetches an object from S3. Returns the body as an io.ReadCloser,
// the ETag string (without surrounding quotes), and any error.
// Caller is responsible for closing the returned body.
func GetObject(ctx context.Context, client *s3.Client, bucket, key string) (io.ReadCloser, string, error) {
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, "", fmt.Errorf("s3: get object %q: %w", key, err)
	}

	etag := ""
	if out.ETag != nil {
		// S3 returns ETags wrapped in double quotes; strip them.
		raw := *out.ETag
		if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
			etag = raw[1 : len(raw)-1]
		} else {
			etag = raw
		}
	}

	return out.Body, etag, nil
}

// StreamObject streams an S3 object directly to an http.ResponseWriter.
// It sets the Content-Type and ETag response headers before copying the body.
func StreamObject(ctx context.Context, client *s3.Client, bucket, key, contentType string, w http.ResponseWriter) error {
	body, etag, err := GetObject(ctx, client, bucket, key)
	if err != nil {
		return err
	}
	defer body.Close()

	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if etag != "" {
		w.Header().Set("ETag", `"`+etag+`"`)
	}

	if _, err := io.Copy(w, body); err != nil {
		return fmt.Errorf("s3: stream object %q: %w", key, err)
	}
	return nil
}
