package storage

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Config holds the configuration needed to build an S3 client.
type S3Config struct {
	Bucket          string
	Region          string
	Endpoint        string // optional; empty means AWS S3
	AccessKeyID     string // optional; empty means IAM role
	SecretAccessKey string // optional; empty means IAM role
	PathStyle       bool   // set true for MinIO and similar providers
}

// S3Client wraps the AWS SDK S3 client with bucket information.
type S3Client struct {
	Client *s3.Client
	Bucket string
}

// NewS3Client constructs an S3Client from the provided configuration.
//
// Credential resolution order:
//  1. If both AccessKeyID and SecretAccessKey are set, use static credentials.
//  2. Otherwise fall back to the default credential chain (IAM role, env, etc.).
//
// If Endpoint is set, it is used as a custom endpoint (for Backblaze B2, R2,
// MinIO, DigitalOcean Spaces, etc.). PathStyle forces path-style addressing.
func NewS3Client(ctx context.Context, cfg S3Config) (*S3Client, error) {
	var awsCfg aws.Config
	var err error

	loadOpts := []func(*config.LoadOptions) error{
		config.WithRegion(cfg.Region),
	}

	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		loadOpts = append(loadOpts,
			config.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(
					cfg.AccessKeyID,
					cfg.SecretAccessKey,
					"",
				),
			),
		)
	}

	awsCfg, err = config.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	s3Opts := []func(*s3.Options){}

	if cfg.Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		})
	}

	if cfg.PathStyle {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.UsePathStyle = true
		})
	}

	client := s3.NewFromConfig(awsCfg, s3Opts...)

	return &S3Client{
		Client: client,
		Bucket: cfg.Bucket,
	}, nil
}
