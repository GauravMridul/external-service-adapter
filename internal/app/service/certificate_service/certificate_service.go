package certificate_service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net/http"
	"path/filepath"

	commoninit "esa/internal/app/init"
	"esa/internal/app/utility"

	"github.com/dmi-infotech/common-modules/go/contracts"
	pkgerrors "github.com/pkg/errors"
)

// CertificateConfig represents certificate configuration
type CertificateConfig struct {
	Enabled            bool   `json:"enabled"`
	CertificateType    string `json:"certificate_type"` // "file_paths", "certificate_name", "inline_content"
	CertificateName    string `json:"certificate_name,omitempty"`
	CertificatePath    string `json:"certificate_path,omitempty"`
	PrivateKeyPath     string `json:"private_key_path,omitempty"`
	CACertPath         string `json:"ca_cert_path,omitempty"`
	CertificateContent string `json:"certificate_content,omitempty"`
	PrivateKeyContent  string `json:"private_key_content,omitempty"`
	CACertContent      string `json:"ca_cert_content,omitempty"`
	VerifyServerCert   bool   `json:"verify_server_cert"`
	TLSVersion         string `json:"tls_version"`
}

// CertificateService handles certificate management operations
type CertificateService struct {
	logger contracts.Logger
}

// NewCertificateService creates a new certificate service
func NewCertificateService() *CertificateService {
	return &CertificateService{
		logger: commoninit.GetLogger(),
	}
}

// getLog returns the request-scoped logger when set in context, otherwise cs.logger.WithContext(ctx).
func (cs *CertificateService) getLog(ctx context.Context) contracts.Logger {
	if reqLog := utility.GetRequestLogger(ctx); reqLog != nil {
		return reqLog
	}
	return cs.logger.WithContext(ctx)
}

// ConfigureHTTPClientWithCertificate configures HTTP client with client certificates
func (cs *CertificateService) ConfigureHTTPClientWithCertificate(ctx context.Context, client *http.Client, config *CertificateConfig) error {
	log := cs.getLog(ctx)

	if !config.Enabled {
		return nil
	}

	log.Infow("Configuring HTTP client with certificates", "certificate_type", config.CertificateType)

	// Create TLS config
	tlsConfig := &tls.Config{
		InsecureSkipVerify: !config.VerifyServerCert,
	}

	// Set TLS version
	if err := cs.setTLSVersion(tlsConfig, config.TLSVersion); err != nil {
		log.Errorw("Failed to set TLS version", "error", err, "tls_version", config.TLSVersion)
		return pkgerrors.Wrap(err, "failed to set TLS version")
	}

	// Load client certificate based on type
	switch config.CertificateType {
	case "file_paths":
		if err := cs.loadCertificateFromFiles(ctx, tlsConfig, config); err != nil {
			log.Errorw("Failed to load certificate from files", "error", err)
			return pkgerrors.Wrap(err, "failed to load certificate from files")
		}
	case "certificate_name":
		if err := cs.loadCertificateByName(ctx, tlsConfig, config); err != nil {
			log.Errorw("Failed to load certificate by name", "error", err)
			return pkgerrors.Wrap(err, "failed to load certificate by name")
		}
	case "inline_content":
		if err := cs.loadCertificateFromContent(tlsConfig, config); err != nil {
			log.Errorw("Failed to load certificate from content", "error", err)
			return pkgerrors.Wrap(err, "failed to load certificate from content")
		}
	default:
		return fmt.Errorf("unsupported certificate type: %s", config.CertificateType)
	}

	// Configure transport with TLS config
	if client.Transport == nil {
		client.Transport = &http.Transport{}
	}

	if transport, ok := client.Transport.(*http.Transport); ok {
		transport.TLSClientConfig = tlsConfig
		log.Infow("HTTP client configured with certificates successfully",
			"tls_version", config.TLSVersion,
			"verify_server_cert", config.VerifyServerCert)
	} else {
		return fmt.Errorf("client transport is not *http.Transport")
	}

	return nil
}

// loadCertificateFromFiles loads certificate from file paths
func (cs *CertificateService) loadCertificateFromFiles(ctx context.Context, tlsConfig *tls.Config, config *CertificateConfig) error {
	if config.CertificatePath == "" || config.PrivateKeyPath == "" {
		return fmt.Errorf("certificate_path and private_key_path are required for file_paths type")
	}

	// Load client certificate and key
	clientCert, err := tls.LoadX509KeyPair(config.CertificatePath, config.PrivateKeyPath)
	if err != nil {
		return pkgerrors.Wrapf(err, "failed to load client certificate from %s and %s",
			config.CertificatePath, config.PrivateKeyPath)
	}

	tlsConfig.Certificates = []tls.Certificate{clientCert}

	// Load CA certificate if provided
	if config.CACertPath != "" {
		if err := cs.loadCACertFromFile(ctx, tlsConfig, config.CACertPath); err != nil {
			return pkgerrors.Wrap(err, "failed to load CA certificate")
		}
	}

	return nil
}

// loadCertificateByName loads certificate using certificate name (similar to Salesforce Apex)
func (cs *CertificateService) loadCertificateByName(ctx context.Context, tlsConfig *tls.Config, config *CertificateConfig) error {
	if config.CertificateName == "" {
		return fmt.Errorf("certificate_name is required for certificate_name type")
	}

	// Get base certificates path from config
	configProvider := commoninit.GetConfig()
	basePath := "/app/certs" // Default path
	if configProvider != nil {
		if configuredPath := configProvider.GetString("certificates.base_path"); configuredPath != "" {
			basePath = configuredPath
		}
	}

	// Construct file paths based on certificate name
	certPath := filepath.Join(basePath, config.CertificateName+".crt")
	keyPath := filepath.Join(basePath, config.CertificateName+".key")

	// Load client certificate and key
	clientCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return pkgerrors.Wrapf(err, "failed to load certificate by name %s from %s and %s",
			config.CertificateName, certPath, keyPath)
	}

	tlsConfig.Certificates = []tls.Certificate{clientCert}

	// Try to load CA certificate if exists
	caCertPath := filepath.Join(basePath, "ca.crt")
	if cs.fileExists(caCertPath) {
		if err := cs.loadCACertFromFile(ctx, tlsConfig, caCertPath); err != nil {
			cs.getLog(ctx).Warnw("Failed to load CA certificate, continuing without it", "error", err, "path", caCertPath)
		}
	}

	return nil
}

// loadCertificateFromContent loads certificate from inline content
func (cs *CertificateService) loadCertificateFromContent(tlsConfig *tls.Config, config *CertificateConfig) error {
	if config.CertificateContent == "" || config.PrivateKeyContent == "" {
		return fmt.Errorf("certificate_content and private_key_content are required for inline_content type")
	}

	// Load client certificate and key from content
	clientCert, err := tls.X509KeyPair([]byte(config.CertificateContent), []byte(config.PrivateKeyContent))
	if err != nil {
		return pkgerrors.Wrap(err, "failed to load client certificate from inline content")
	}

	tlsConfig.Certificates = []tls.Certificate{clientCert}

	// Load CA certificate if provided
	if config.CACertContent != "" {
		if err := cs.loadCACertFromContent(tlsConfig, config.CACertContent); err != nil {
			return pkgerrors.Wrap(err, "failed to load CA certificate from content")
		}
	}

	return nil
}

// loadCACertFromFile loads CA certificate from file
func (cs *CertificateService) loadCACertFromFile(ctx context.Context, tlsConfig *tls.Config, caCertPath string) error {
	caCertBytes, err := ioutil.ReadFile(caCertPath)
	if err != nil {
		return pkgerrors.Wrapf(err, "failed to read CA certificate file %s", caCertPath)
	}

	return cs.loadCACertFromBytes(tlsConfig, caCertBytes)
}

// loadCACertFromContent loads CA certificate from content
func (cs *CertificateService) loadCACertFromContent(tlsConfig *tls.Config, caCertContent string) error {
	return cs.loadCACertFromBytes(tlsConfig, []byte(caCertContent))
}

// loadCACertFromBytes loads CA certificate from bytes
func (cs *CertificateService) loadCACertFromBytes(tlsConfig *tls.Config, caCertBytes []byte) error {
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCertBytes) {
		return fmt.Errorf("failed to parse CA certificate")
	}
	tlsConfig.RootCAs = caCertPool
	return nil
}

// setTLSVersion sets the TLS version on the TLS config
func (cs *CertificateService) setTLSVersion(tlsConfig *tls.Config, version string) error {
	switch version {
	case "1.0":
		tlsConfig.MinVersion = tls.VersionTLS10
		tlsConfig.MaxVersion = tls.VersionTLS10
	case "1.1":
		tlsConfig.MinVersion = tls.VersionTLS11
		tlsConfig.MaxVersion = tls.VersionTLS11
	case "1.2":
		tlsConfig.MinVersion = tls.VersionTLS12
		tlsConfig.MaxVersion = tls.VersionTLS12
	case "1.3":
		tlsConfig.MinVersion = tls.VersionTLS13
		tlsConfig.MaxVersion = tls.VersionTLS13
	case "":
		// Use default TLS version (latest supported)
		tlsConfig.MinVersion = tls.VersionTLS12 // Minimum secure version
	default:
		return fmt.Errorf("unsupported TLS version: %s", version)
	}
	return nil
}

// fileExists checks if a file exists
func (cs *CertificateService) fileExists(filename string) bool {
	_, err := ioutil.ReadFile(filename)
	return err == nil
}

// CreateHTTPClientWithCertificate creates a new HTTP client with certificate configuration
func (cs *CertificateService) CreateHTTPClientWithCertificate(ctx context.Context, config *CertificateConfig) (*http.Client, error) {
	client := &http.Client{
		Transport: &http.Transport{},
	}

	if config != nil && config.Enabled {
		if err := cs.ConfigureHTTPClientWithCertificate(ctx, client, config); err != nil {
			return nil, err
		}
	}

	return client, nil
}

// ValidateCertificateConfig validates certificate configuration
func (cs *CertificateService) ValidateCertificateConfig(config *CertificateConfig) error {
	if !config.Enabled {
		return nil
	}

	switch config.CertificateType {
	case "file_paths":
		if config.CertificatePath == "" || config.PrivateKeyPath == "" {
			return fmt.Errorf("certificate_path and private_key_path are required for file_paths type")
		}
	case "certificate_name":
		if config.CertificateName == "" {
			return fmt.Errorf("certificate_name is required for certificate_name type")
		}
	case "inline_content":
		if config.CertificateContent == "" || config.PrivateKeyContent == "" {
			return fmt.Errorf("certificate_content and private_key_content are required for inline_content type")
		}
	default:
		return fmt.Errorf("unsupported certificate type: %s (supported: file_paths, certificate_name, inline_content)", config.CertificateType)
	}

	// Validate TLS version
	validTLSVersions := []string{"", "1.0", "1.1", "1.2", "1.3"}
	valid := false
	for _, v := range validTLSVersions {
		if config.TLSVersion == v {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("unsupported TLS version: %s (supported: 1.0, 1.1, 1.2, 1.3)", config.TLSVersion)
	}

	return nil
}
