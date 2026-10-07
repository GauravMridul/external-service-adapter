package s3_service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	commoninit "esa/internal/app/init"
	"esa/internal/app/utility"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsConfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/dmi-infotech/common-modules/go/contracts"
	pkgerrors "github.com/pkg/errors"
)

// S3Config represents S3 configuration
type S3Config struct {
	Enabled         bool                   `json:"enabled"`
	AWSAccessKey    string                 `json:"aws_access_key"`
	AWSSecretKey    string                 `json:"aws_secret_key"`
	Region          string                 `json:"region"`
	BucketName      string                 `json:"bucket_name"`
	KeyPrefix       string                 `json:"key_prefix"`
	FileName        string                 `json:"file_name,omitempty"`   // optional; supports expressions, e.g. <Contact.Id>
	UploadThreshold int64                  `json:"upload_threshold"`
	RetentionDays   int                    `json:"retention_days"`
	Compress        bool                   `json:"compress"`
	ContentType     string                 `json:"content_type,omitempty"` // optional; e.g. text/plain (default application/json)
	ACL             string                 `json:"acl,omitempty"`          // optional; e.g. bucket-owner-full-control
	UploadRawBody   bool                   `json:"upload_raw_body,omitempty"` // if true, upload response as-is without JSON marshal
	Metadata        map[string]interface{} `json:"metadata"`
}

// S3Service handles S3 operations
type S3Service struct {
	logger contracts.Logger
}

// NewS3Service creates a new S3 service
func NewS3Service() *S3Service {
	return &S3Service{
		logger: commoninit.GetLogger(),
	}
}

// getLog returns the request-scoped logger when set in context, otherwise s.logger.WithContext(ctx).
func (s *S3Service) getLog(ctx context.Context) contracts.Logger {
	if reqLog := utility.GetRequestLogger(ctx); reqLog != nil {
		return reqLog
	}
	return s.logger.WithContext(ctx)
}

// UploadResponse uploads response to S3 and returns the S3 URL
func (s *S3Service) UploadResponse(ctx context.Context, config *S3Config, responseData interface{}, rawBody string, serviceName string, correlationID string) (string, error) {
	log := s.getLog(ctx)

	if !config.Enabled {
		return "", nil
	}

	var responseBytes []byte
	if config.UploadRawBody && rawBody != "" {
		responseBytes = []byte(rawBody)
	} else {
		var err error
		responseBytes, err = json.Marshal(responseData)
		if err != nil {
			log.Errorw("Failed to marshal response data", "error", err)
			return "", pkgerrors.Wrap(err, "failed to marshal response data")
		}
	}

	if int64(len(responseBytes)) < config.UploadThreshold {
		log.Debugw("Response size below threshold, not uploading to S3",
			"size", len(responseBytes),
			"threshold", config.UploadThreshold)
		return "", nil
	}

	var finalData []byte
	contentType := s.resolveContentType(config)
	if config.Compress {
		compressed, err := s.compressData(responseBytes)
		if err != nil {
			log.Warnw("Failed to compress data, using uncompressed", "error", err)
			finalData = responseBytes
		} else {
			finalData = compressed
			contentType = "application/gzip"
			log.Debugw("Data compressed", "original_size", len(responseBytes), "compressed_size", len(finalData))
		}
	} else {
		finalData = responseBytes
	}

	s3Client, err := s.createS3Client(config)
	if err != nil {
		log.Errorw("Failed to create S3 client", "error", err)
		return "", pkgerrors.Wrap(err, "failed to create S3 client")
	}

	s3Key := s.buildS3Key(config.KeyPrefix, config.FileName, serviceName, correlationID)

	metadata := make(map[string]string)
	for key, value := range config.Metadata {
		if strValue, ok := value.(string); ok {
			metadata[key] = strValue
		}
	}
	metadata["upload_timestamp"] = time.Now().UTC().Format(time.RFC3339)
	metadata["service_name"] = serviceName
	metadata["correlation_id"] = correlationID
	metadata["original_size"] = fmt.Sprintf("%d", len(responseBytes))
	if config.Compress {
		metadata["compressed"] = "true"
		metadata["compressed_size"] = fmt.Sprintf("%d", len(finalData))
	}

	putInput := &s3.PutObjectInput{
		Bucket:      aws.String(config.BucketName),
		Key:         aws.String(s3Key),
		Body:        bytes.NewReader(finalData),
		ContentType: aws.String(contentType),
		Metadata:    metadata,
	}
	if config.ACL != "" {
		putInput.ACL = types.ObjectCannedACL(config.ACL)
	}

	if _, err = s3Client.PutObject(ctx, putInput); err != nil {
		log.Errorw("Failed to upload to S3", "error", err, "bucket", config.BucketName, "key", s3Key)
		return "", pkgerrors.Wrap(err, "failed to upload to S3")
	}

	s3URL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", config.BucketName, config.Region, s3Key)
	log.Infow("Successfully uploaded response to S3",
		"s3_url", s3URL,
		"original_size", len(responseBytes),
		"final_size", len(finalData),
		"compressed", config.Compress)
	return s3URL, nil
}

func (s *S3Service) resolveContentType(config *S3Config) string {
	if config.ContentType != "" {
		return config.ContentType
	}
	if config.Compress {
		return "application/gzip"
	}
	return "application/json"
}

// buildS3Key returns key_prefix + file_name when file_name is set, else key_prefix + generated filename
func (s *S3Service) buildS3Key(keyPrefix, fileName, serviceName, correlationID string) string {
	if fileName != "" {
		prefix := strings.TrimSuffix(keyPrefix, "/")
		if prefix == "" {
			return fileName
		}
		return prefix + "/" + fileName
	}
	return s.generateS3Key(keyPrefix, serviceName, correlationID)
}

// createS3Client creates S3 client with dynamic credentials
func (s *S3Service) createS3Client(config *S3Config) (*s3.Client, error) {
	cfg, err := awsConfig.LoadDefaultConfig(context.TODO(),
		awsConfig.WithRegion(config.Region),
		awsConfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			config.AWSAccessKey,
			config.AWSSecretKey,
			"",
		)),
	)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "failed to load AWS config")
	}

	return s3.NewFromConfig(cfg), nil
}

// generateS3Key generates a unique S3 key for the response
func (s *S3Service) generateS3Key(keyPrefix, serviceName, correlationID string) string {
	now := time.Now().UTC()

	// Replace placeholders in key prefix
	keyPrefix = strings.ReplaceAll(keyPrefix, "{service_name}", serviceName)
	keyPrefix = strings.ReplaceAll(keyPrefix, "{date}", now.Format("2006-01-02"))
	keyPrefix = strings.ReplaceAll(keyPrefix, "{year}", now.Format("2006"))
	keyPrefix = strings.ReplaceAll(keyPrefix, "{month}", now.Format("01"))
	keyPrefix = strings.ReplaceAll(keyPrefix, "{day}", now.Format("02"))

	// Generate unique filename
	timestamp := now.Format("20060102-150405")
	filename := fmt.Sprintf("%s_%s_%s.json", serviceName, correlationID, timestamp)
	if strings.HasSuffix(keyPrefix, "/") {
		return keyPrefix + filename
	}
	return keyPrefix + "/" + filename
}

// compressData compresses data using gzip
func (s *S3Service) compressData(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gzipWriter := gzip.NewWriter(&buf)

	_, err := gzipWriter.Write(data)
	if err != nil {
		return nil, err
	}

	err = gzipWriter.Close()
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// ShouldUploadToS3 checks if response should be uploaded to S3
func (s *S3Service) ShouldUploadToS3(config *S3Config, responseSize int64) bool {
	return config != nil && config.Enabled && responseSize >= config.UploadThreshold
}
