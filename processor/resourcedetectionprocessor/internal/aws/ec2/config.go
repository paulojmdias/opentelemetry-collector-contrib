// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package ec2 // import "github.com/open-telemetry/opentelemetry-collector-contrib/processor/resourcedetectionprocessor/internal/aws/ec2"

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/retry"

	"github.com/open-telemetry/opentelemetry-collector-contrib/processor/resourcedetectionprocessor/internal/aws/ec2/internal/metadata"
)

// Config defines user-specified configurations unique to the EC2 detector
type Config struct {
	// Tags is a list of regex's to match ec2 instance tag keys that users want
	// to add as resource attributes to processed data
	Tags               []string                          `mapstructure:"tags"`
	ResourceAttributes metadata.ResourceAttributesConfig `mapstructure:"resource_attributes"`
	// Deprecated: MaxAttempts configures AWS SDK HTTP-level retries for EC2/IMDS API calls.
	// This controls low-level SDK retries within a single Detect() call, which is semantically
	// different from the processor-level retry.max_elapsed_time that controls how many times
	// Detect() itself is retried on failure. This field remains functional but may be removed
	// in a future release.
	MaxAttempts int `mapstructure:"max_attempts"`
	// Deprecated: MaxBackoff configures the maximum backoff duration for AWS SDK HTTP-level
	// retries within a single Detect() call. Use the processor-level retry.max_interval
	// to control backoff between Detect() retries.
	MaxBackoff            time.Duration `mapstructure:"max_backoff"`
	FailOnMissingMetadata bool          `mapstructure:"fail_on_missing_metadata"`
	// TagsFromIMDS controls whether instance tags are fetched via IMDS (true)
	// or via the EC2 DescribeTags API (false, default).
	// IMDS does not require IAM permissions but requires InstanceMetadataTags=enabled on the instance.
	// Note: IMDS must be network-accessible from the process; workloads running in containers
	// (e.g., EKS pods, ECS tasks) may not have access to IMDS even if it is enabled on the host,
	// unless the instance metadata options are configured to allow container access.
	TagsFromIMDS bool `mapstructure:"tags_from_imds"`
}

func CreateDefaultConfig() Config {
	return Config{
		Tags:               []string{},
		ResourceAttributes: metadata.DefaultResourceAttributesConfig(),
		MaxAttempts:        retry.DefaultMaxAttempts,
		MaxBackoff:         retry.DefaultMaxBackoff,
	}
}
