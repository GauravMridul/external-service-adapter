package sequence_service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"esa/internal/app/constants"
	"esa/internal/app/dto/common_dto"
	"esa/internal/app/dto/request_dto/esa_request_dto"
	commoninit "esa/internal/app/init"
	"esa/internal/app/models"
	"esa/internal/app/service/certificate_service"
	"esa/internal/app/service/s3_service"
	"esa/internal/app/service/token_service"
	"esa/internal/app/utility"

	"github.com/dmi-infotech/common-modules/go/contracts"
	"github.com/google/uuid"
	pkgerrors "github.com/pkg/errors"
)

// ServiceInvoker handles the actual invocation of external services
type ServiceInvoker struct {
	apiClient           contracts.APIClient
	expressionProcessor *ExpressionProcessor
	logger              contracts.Logger
	s3Service           *s3_service.S3Service
	tokenService        *token_service.TokenService
	certificateService  *certificate_service.CertificateService
}

// NewServiceInvoker creates a new service invoker
func NewServiceInvoker(apiClient contracts.APIClient, expressionProcessor *ExpressionProcessor) *ServiceInvoker {
	return &ServiceInvoker{
		apiClient:           apiClient,
		expressionProcessor: expressionProcessor,
		logger:              commoninit.GetLogger(),
		s3Service:           s3_service.NewS3Service(),
		tokenService:        token_service.NewTokenService(),
		certificateService:  certificate_service.NewCertificateService(),
	}
}

// getLog returns the request-scoped logger when set in context, otherwise si.logger.WithContext(ctx).
func (si *ServiceInvoker) getLog(ctx context.Context) contracts.Logger {
	if reqLog := utility.GetRequestLogger(ctx); reqLog != nil {
		return reqLog
	}
	return si.logger.WithContext(ctx)
}

// FanOutFilterCondition represents a single filter condition (include or exclude)
type FanOutFilterCondition struct {
	Field string      `json:"field"`
	Op    string      `json:"op"` // eq, ne, in, not_in, ==, !=
	Value interface{} `json:"value"`
}

// FanOutConditionNode is a node in the condition tree: either a leaf (field/op/value) or a group (and/or with children).
// When "and" or "or" is present and non-empty, the node is evaluated as a group; otherwise as a leaf condition.
type FanOutConditionNode struct {
	Field string                `json:"field,omitempty"`
	Op    string                `json:"op,omitempty"`
	Value interface{}           `json:"value,omitempty"`
	And   []FanOutConditionNode `json:"and,omitempty"`
	Or    []FanOutConditionNode `json:"or,omitempty"`
}

// FanOutFilter represents include (AND) or exclude (OR) conditions, or a single condition tree (and/or mixing).
// If Condition is set, it is used and include/exclude are ignored. Otherwise include (AND) and exclude (OR) apply.
type FanOutFilter struct {
	Include   []FanOutFilterCondition `json:"include,omitempty"`
	Exclude   []FanOutFilterCondition `json:"exclude,omitempty"`
	Condition *FanOutConditionNode    `json:"condition,omitempty"`
}

// FanOutSource identifies the source service and array path for fan-out
type FanOutSource struct {
	ServiceID   int    `json:"service_id,omitempty"`
	ServiceName string `json:"service_name,omitempty"`
	ArrayPath   string `json:"array_path,omitempty"` // e.g. "result"
}

// FanOutItemBinding holds per-item overrides for request (from ((item.xxx)))
type FanOutItemBinding struct {
	RequestBody map[string]string `json:"request_body,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

// FanOutCollation configures how N responses are combined
type FanOutCollation struct {
	OutputKey string `json:"output_key"`     // e.g. "results"
	Merge     string `json:"merge"`          // "full" or "path"
	Path      string `json:"path,omitempty"` // when merge=="path", e.g. "result"
}

// FanOutFromArrayConfig is the additional_config.fan_out_from_array block
type FanOutFromArrayConfig struct {
	Enabled     bool              `json:"enabled"`
	Source      FanOutSource      `json:"source"`
	Filter      *FanOutFilter     `json:"filter,omitempty"`
	ItemBinding FanOutItemBinding `json:"item_binding,omitempty"`
	Collation   FanOutCollation   `json:"collation"`
}

// PlaceholderCleanupFieldConfig lists post-processing steps to skip for one request slot (body key, header name, or URL).
// See PlaceholderCleanupOptions in expression_processor.go for valid "omit" step IDs.
type PlaceholderCleanupFieldConfig struct {
	Omit []string `json:"omit,omitempty"`
}

// PlaceholderCleanupConfig is additional_config.placeholder_cleanup: per-field control of final placeholder cleanup.
// Only top-level request_body keys are matched; nested map values use default cleanup unless structured differently in a future version.
type PlaceholderCleanupConfig struct {
	RequestBody map[string]PlaceholderCleanupFieldConfig `json:"request_body,omitempty"`
	Headers     map[string]PlaceholderCleanupFieldConfig `json:"headers,omitempty"`
	URL         *PlaceholderCleanupFieldConfig           `json:"url,omitempty"`
}

// AdditionalConfig represents the additional_config structure
type AdditionalConfig struct {
	S3ResponseUpload   *s3_service.S3Config                   `json:"s3_response_upload,omitempty"`
	TokenManagement    *token_service.TokenConfig             `json:"token_management,omitempty"`
	ClientCertificate  *certificate_service.CertificateConfig `json:"client_certificate,omitempty"`
	EnableRawBase64    bool                                   `json:"enable_raw_base64,omitempty"`
	FanOutFromArray    *FanOutFromArrayConfig                 `json:"fan_out_from_array,omitempty"`
	PlaceholderCleanup *PlaceholderCleanupConfig              `json:"placeholder_cleanup,omitempty"`
	// Existing configurations
	PreExecution  map[string]interface{} `json:"PreExecution,omitempty"`
	PostExecution map[string]interface{} `json:"PostExecution,omitempty"`
	Cache         map[string]interface{} `json:"cache,omitempty"`
	Retry         map[string]interface{} `json:"retry,omitempty"`
}

// fieldConfigToCleanupOptions builds expression processor options from DB JSON (nil if nothing to omit).
func fieldConfigToCleanupOptions(fc *PlaceholderCleanupFieldConfig) *PlaceholderCleanupOptions {
	if fc == nil || len(fc.Omit) == 0 {
		return nil
	}
	cp := make([]string, len(fc.Omit))
	copy(cp, fc.Omit)
	return &PlaceholderCleanupOptions{Omit: cp}
}

// InvokeService executes a single service call with enhanced features
func (si *ServiceInvoker) InvokeService(ctx context.Context, request *esa_request_dto.ProcessSequenceRequest, masterDTO *common_dto.MasterDTO, esaLog *models.EsaLog) error {
	log := si.getLog(ctx)
	log.Infow("Executing service", "serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName)

	// Parse additional_config if available (before request preparation so placeholder_cleanup applies)
	additionalConfig, err := si.parseAdditionalConfig(esaLog)
	if err != nil {
		log.Warnw("Failed to parse additional_config, continuing without enhanced features", "error", err)
		// Continue with empty config - will use basic flow
		additionalConfig = &AdditionalConfig{}
	}

	// Generate correlation ID for this request
	correlationID := si.generateCorrelationID(request)

	// Step 1: Always process template variables in request body, URL, and headers
	// This ensures request body is prepared regardless of whether we use pre-configured response or API call
	si.prepareRequestBody(ctx, esaLog, masterDTO, additionalConfig)

	// If the request deadline expired while building the body (e.g. a pathological large-payload
	// resolution), abort before any network call. The engine returns best-effort text on deadline;
	// this turns that into a clean error that propagates to a 504 instead of dispatching a partial body.
	if err := ctx.Err(); err != nil {
		log.Warnw("Request preparation aborted: context deadline exceeded/canceled",
			"serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName, "error", err)
		return pkgerrors.Wrap(err, "request preparation canceled before dispatch")
	}

	// Step 2: Check for pre-configured response before proceeding with API call
	if si.shouldUsePreConfiguredResponse(esaLog) {
		return si.processPreConfiguredResponse(ctx, esaLog, correlationID)
	}

	// Step 3: Handle token management (if configured)
	if additionalConfig.TokenManagement != nil && additionalConfig.TokenManagement.Enabled {
		if err := si.handleTokenManagement(ctx, esaLog, additionalConfig, correlationID); err != nil {
			log.Errorw("Token management failed", "error", err)
			return pkgerrors.Wrap(err, "token management failed")
		}
	}

	// Step 4: Create HTTP client with certificate support (if configured)
	httpClient, err := si.createHTTPClientWithCertificates(ctx, additionalConfig)
	if err != nil {
		log.Errorw("Failed to create HTTP client with certificates", "error", err)
		return pkgerrors.Wrap(err, "failed to create HTTP client with certificates")
	}

	// Step 5: Execute service with retry logic and 401 handling
	callStart := time.Now()
	statusCode, responseBody, err := si.executeServiceWithRetry(ctx, esaLog, httpClient, additionalConfig, correlationID)
	serviceDurationMs := time.Since(callStart).Milliseconds()
	requestStartTime := utility.GetRequestStartTime(ctx)
	if requestStartTime.IsZero() {
		requestStartTime = callStart
	}
	elapsedSinceStartMs := time.Since(requestStartTime).Milliseconds()
	groupIndex := utility.GetGroupIndex(ctx)
	groupLabel := ""
	if groupIndex >= 0 {
		groupLabel = fmt.Sprintf("group_%d", groupIndex+1)
	}
	errorMsg := ""
	if err != nil {
		errorMsg = err.Error()
	}
	log.Infow("Checkpoint",
		"checkpoint", "service_invoked",
		"service_name", esaLog.ServiceName,
		"service_url", esaLog.Request.URL,
		"http_status_code", statusCode,
		"service_duration_ms", serviceDurationMs,
		"elapsed_since_start_ms", elapsedSinceStartMs,
		"group_index", groupIndex,
		"group_label", groupLabel,
		"error", errorMsg)
	if err != nil {
		log.Errorw("Service execution failed", "error", err)
		esaLog.Status = "FAILED"
		return pkgerrors.Wrap(err, "service execution failed")
	}

	// Step 6: Process response and handle S3 upload (if configured)
	if err := si.processServiceResponse(ctx, esaLog, responseBody, statusCode, additionalConfig, correlationID, masterDTO); err != nil {
		log.Errorw("Response processing failed", "error", err)
		return pkgerrors.Wrap(err, "response processing failed")
	}

	log.Infow("Service execution completed successfully",
		"serviceID", esaLog.ServiceId,
		"statusCode", statusCode,
		"correlationID", correlationID)

	return nil
}

// parseAdditionalConfig parses the additional_config JSON field
func (si *ServiceInvoker) parseAdditionalConfig(esaLog *models.EsaLog) (*AdditionalConfig, error) {
	if esaLog.ExtraFields == nil {
		return &AdditionalConfig{}, nil
	}

	additionalConfigField, exists := esaLog.ExtraFields["additional_config"]
	if !exists {
		return &AdditionalConfig{}, nil
	}

	// Convert to JSON string if it's a map
	var jsonData []byte
	var err error

	switch v := additionalConfigField.(type) {
	case string:
		jsonData = []byte(v)
	case map[string]interface{}:
		jsonData, err = json.Marshal(v)
		if err != nil {
			return nil, err
		}
	default:
		return &AdditionalConfig{}, nil
	}

	var additionalConfig AdditionalConfig
	if err := json.Unmarshal(jsonData, &additionalConfig); err != nil {
		return nil, err
	}

	return &additionalConfig, nil
}

// handleTokenManagement handles token generation and validation
func (si *ServiceInvoker) handleTokenManagement(ctx context.Context, esaLog *models.EsaLog, additionalConfig *AdditionalConfig, correlationID string) error {
	if additionalConfig.TokenManagement == nil || !additionalConfig.TokenManagement.Enabled {
		return nil
	}

	log := si.getLog(ctx)
	log.Debugw("Handling token management", "correlationID", correlationID)

	// Process placeholders in token config
	tokenConfig := si.processTokenConfigPlaceholders(additionalConfig.TokenManagement)

	// Get valid token
	token, err := si.tokenService.GetValidToken(ctx, tokenConfig)
	if err != nil {
		return pkgerrors.Wrap(err, "failed to get valid token")
	}

	if token != "" {
		// Replace {{TOKEN_PLACEHOLDER}} in headers with actual token
		si.injectTokenIntoHeaders(esaLog, token)
		log.Debugw("Token injected into headers", "correlationID", correlationID)
	}

	return nil
}

// createHTTPClientWithCertificates creates HTTP client with certificate support
func (si *ServiceInvoker) createHTTPClientWithCertificates(ctx context.Context, additionalConfig *AdditionalConfig) (*http.Client, error) {
	if additionalConfig.ClientCertificate == nil || !additionalConfig.ClientCertificate.Enabled {
		// Return default HTTP client
		return &http.Client{}, nil
	}

	log := si.getLog(ctx)
	log.Debugw("Creating HTTP client with certificates")

	// Validate certificate configuration
	if err := si.certificateService.ValidateCertificateConfig(additionalConfig.ClientCertificate); err != nil {
		return nil, pkgerrors.Wrap(err, "invalid certificate configuration")
	}

	// Create HTTP client with certificates
	client, err := si.certificateService.CreateHTTPClientWithCertificate(ctx, additionalConfig.ClientCertificate)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "failed to create HTTP client with certificates")
	}

	return client, nil
}

// executeServiceWithRetry executes service with retry logic and 401 handling
func (si *ServiceInvoker) executeServiceWithRetry(ctx context.Context, esaLog *models.EsaLog, httpClient *http.Client, additionalConfig *AdditionalConfig, correlationID string) (int, string, error) {
	log := si.getLog(ctx)

	// Determine timeout
	timeout := si.getServiceTimeout(esaLog)

	maxRetries := 1 // Default no retries
	if additionalConfig.TokenManagement != nil && additionalConfig.TokenManagement.Enabled && additionalConfig.TokenManagement.RetryOn401 {
		maxRetries = additionalConfig.TokenManagement.MaxTokenRetries
		if maxRetries <= 0 {
			maxRetries = 2 // Default max retries
		}
	}

	var lastErr error
	var statusCode int
	var responseBody string

	for attempt := 1; attempt <= maxRetries; attempt++ {
		log.Debugw("Executing service", "attempt", attempt, "maxRetries", maxRetries, "correlationID", correlationID)

		// Record start time
		startTime := time.Now()

		// Execute the service
		statusCode, responseBody, lastErr = si.executeHTTPRequest(ctx, esaLog, httpClient, timeout)

		// Record end time and execution details
		endTime := time.Now()
		executionTime := int(endTime.Sub(startTime).Milliseconds())

		esaLog.StartTime = startTime
		esaLog.EndTime = endTime
		esaLog.TimeTaken = float64(executionTime)

		// Check if request was successful
		if lastErr == nil {
			// Check for 401 unauthorized and token refresh capability
			if statusCode == 401 && additionalConfig.TokenManagement != nil &&
				additionalConfig.TokenManagement.Enabled && additionalConfig.TokenManagement.RetryOn401 &&
				attempt < maxRetries {

				log.Infow("Received 401 unauthorized, refreshing token", "attempt", attempt, "correlationID", correlationID)

				// Refresh token
				newToken, tokenErr := si.tokenService.RefreshTokenOn401(ctx, additionalConfig.TokenManagement)
				if tokenErr != nil {
					log.Errorw("Failed to refresh token", "error", tokenErr, "correlationID", correlationID)
					continue // Try again with old token or fail
				}

				if newToken != "" {
					// Inject new token into headers
					si.injectTokenIntoHeaders(esaLog, newToken)
					log.Debugw("New token injected, retrying request", "attempt", attempt, "correlationID", correlationID)
					continue // Retry with new token
				}
			}

			// Success or non-401 error
			break
		} else {
			// A live call failed — record a classified error status on esaLog (BUG-3/BUG-4).
			si.applyExecutionError(esaLog, lastErr)
		}

		log.Warnw("Service execution attempt failed", "attempt", attempt, "error", lastErr, "correlationID", correlationID)
	}

	return statusCode, responseBody, lastErr
}

// applyExecutionError records a failed live call on esaLog. It replaces any previously staged
// response body — e.g. a pre-configured response_body / fallback mock that stamped StatusCode=200 at
// load time — with a fresh error body (BUG-4), then sets a non-2xx status code classified from err.
//
// BUG-4: the status code is forced to a non-2xx value regardless of any pre-set value, so a failed
// live call can never be reported with a success code (previously a stale 200 survived because the
// overwrite was guarded by "== 0").
//
// BUG-3: genuine Go timeouts ("context deadline exceeded", "... (Client.Timeout exceeded while
// awaiting headers)") are detected by type via isTimeoutError, so they are labeled 408 instead of
// falling through to a generic failure (the old case-sensitive "timeout" substring check missed them).
func (si *ServiceInvoker) applyExecutionError(esaLog *models.EsaLog, err error) {
	// Fresh error body — discards any pre-set fallback/mock body.
	esaLog.Response.Body = map[string]interface{}{}

	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}

	switch {
	case isTimeoutError(err):
		esaLog.Response.Body["error"] = "Request timeout"
		esaLog.Response.Body["error_details"] = errMsg
		esaLog.Response.StatusCode = 408 // Request Timeout
	case isConnectionRefusedError(err):
		esaLog.Response.Body["error"] = "Connection refused"
		esaLog.Response.Body["error_details"] = errMsg
		esaLog.Response.StatusCode = 503 // Service Unavailable
	default:
		esaLog.Response.Body["error"] = "Request failed"
		esaLog.Response.Body["error_details"] = errMsg
		// Force a non-2xx status: only overwrite a success/redirect code (or unset 0), but never
		// downgrade an already-recorded error status (e.g. a real 4xx/5xx from the server).
		if esaLog.Response.StatusCode < 400 {
			esaLog.Response.StatusCode = 500 // Internal Server Error
		}
	}
}

// isTimeoutError reports whether err represents a request/connection timeout. It uses typed
// detection first (context deadline, net.Error.Timeout()) because Go's real timeout errors do not
// contain the lowercase substring "timeout" — e.g. "context deadline exceeded" and
// "... (Client.Timeout exceeded while awaiting headers)". A case-insensitive string check is kept
// as a defensive fallback for wrapped errors whose type information has been lost.
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "client.timeout")
}

// isConnectionRefusedError reports whether err represents a refused TCP connection, via typed
// detection (syscall.ECONNREFUSED) with a case-insensitive string fallback for wrapped errors.
func isConnectionRefusedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "connection refused")
}

// executeHTTPRequest executes the actual HTTP request
func (si *ServiceInvoker) executeHTTPRequest(ctx context.Context, esaLog *models.EsaLog, httpClient *http.Client, timeout int) (int, string, error) {
	// Add nil pointer protection
	if esaLog == nil {
		return 0, "", pkgerrors.New("esaLog is nil")
	}
	if esaLog.Request.Headers == nil {
		esaLog.Request.Headers = make(map[string]string)
	}
	if esaLog.Request.Body == nil {
		esaLog.Request.Body = make(map[string]interface{})
	}

	// Create HTTP request with content-type specific processing
	httpRequest, err := si.apiClient.CreateRequestWithContentType(
		ctx,
		esaLog.Request.Method,
		esaLog.Request.URL,
		esaLog.Request.Headers,
		esaLog.Request.Body,
	)
	if err != nil {
		return 0, "", pkgerrors.Wrap(err, "failed to create HTTP request")
	}

	// Use custom HTTP client if provided, otherwise use default API client
	if httpClient != nil && httpClient.Transport != nil {
		// Execute with custom client
		httpClient.Timeout = time.Duration(timeout) * time.Second
		resp, err := httpClient.Do(httpRequest)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()

		// Read response body safely regardless of Content-Length/chunked encoding
		var responseBody string
		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return resp.StatusCode, "", readErr
		}
		responseBody = string(bodyBytes)

		return resp.StatusCode, responseBody, nil
	}

	// Use default API client with connection retry on connection errors only (not on timeout)
	maxConnectionRetries := commoninit.GetConfigInt(constants.HttpConnectionRetryCountKey, constants.DefaultConnectionRetryCount)
	return si.apiClient.RestExecuteWithConnectionRetry(ctx, httpRequest, timeout, maxConnectionRetries)
}

// normalizeJSONNaN returns a copy of s with all unquoted-style NaN replaced by 0 so that
// Go's json.Unmarshal can parse responses from APIs that emit NaN (e.g. Analytics AA).
func normalizeJSONNaN(s string) string {
	return strings.ReplaceAll(s, "NaN", "0")
}

// processServiceResponse processes the service response and handles S3 upload
func (si *ServiceInvoker) processServiceResponse(ctx context.Context, esaLog *models.EsaLog, responseBody string, statusCode int, additionalConfig *AdditionalConfig, correlationID string, masterDTO *common_dto.MasterDTO) error {
	log := si.getLog(ctx)

	// Parse response body into JSON if possible
	var responseData interface{}
	rawBase64 := ""
	if additionalConfig != nil && additionalConfig.EnableRawBase64 {
		rawBase64 = base64.StdEncoding.EncodeToString([]byte(responseBody))
		if esaLog.ExtraFields == nil {
			esaLog.ExtraFields = make(map[string]interface{})
		}
		esaLog.ExtraFields["response_raw_base64"] = rawBase64
	}

	if responseBody != "" {
		var responseJSON map[string]interface{}
		if jsonErr := json.Unmarshal([]byte(responseBody), &responseJSON); jsonErr == nil {
			responseData = responseJSON
			// Preserve raw base64 alongside parsed JSON
			if responseJSON == nil {
				responseJSON = make(map[string]interface{})
			}
			if rawBase64 != "" {
				responseJSON["_raw_base64"] = rawBase64
			}
			esaLog.Response.Body = responseJSON
		} else {
			// First parse failed (e.g. body contains NaN). Try normalizing NaN->0 so ((Service.Data__c)) etc. resolve.
			normalized := normalizeJSONNaN(responseBody)
			if jsonErr2 := json.Unmarshal([]byte(normalized), &responseJSON); jsonErr2 == nil {
				responseData = responseJSON
				if responseJSON == nil {
					responseJSON = make(map[string]interface{})
				}
				if rawBase64 != "" {
					responseJSON["_raw_base64"] = rawBase64
				}
				esaLog.Response.Body = responseJSON
			} else {
				// Still not valid JSON, store as raw_response
				responseData = responseBody
				esaLog.Response.Body = make(map[string]interface{})
				if rawBase64 != "" {
					esaLog.Response.Body["_raw_base64"] = rawBase64
				} else {
					esaLog.Response.Body["raw_response"] = responseBody
				}
			}
		}
	} else {
		// Ensure response body is not nil
		esaLog.Response.Body = make(map[string]interface{})
		if rawBase64 != "" {
			esaLog.Response.Body["_raw_base64"] = rawBase64
		}
	}

	// Store status code
	esaLog.Response.StatusCode = statusCode

	// Update service status based on HTTP status code
	// Only 2xx status codes should be marked as successful
	if statusCode >= 200 && statusCode < 300 {
		esaLog.Status = "COMPLETED"
		// Stamp source so plug_response_into (apply_to: api_call) can wrap this body.
		// Only 2xx is stamped, so HTTP error/timeout/failure bodies are never wrapped.
		esaLog.Response.Source = responseSourceAPICall
	} else {
		esaLog.Status = "FAILED"
		log.Warnw("Service failed with non-2xx status code",
			"serviceID", esaLog.ServiceId,
			"serviceName", esaLog.ServiceName,
			"statusCode", statusCode,
			"correlationID", correlationID)
	}

	// Handle S3 upload if configured
	if additionalConfig.S3ResponseUpload != nil && additionalConfig.S3ResponseUpload.Enabled {
		log.Debugw("Processing S3 response upload", "correlationID", correlationID)

		s3Config := si.processS3ConfigPlaceholders(additionalConfig.S3ResponseUpload, masterDTO, esaLog)
		rawBody := ""
		if s3Config.UploadRawBody {
			rawBody = responseBody
		}
		s3URL, err := si.s3Service.UploadResponse(ctx, s3Config, responseData, rawBody, esaLog.ServiceName, correlationID)
		if err != nil {
			log.Errorw("Failed to upload response to S3", "error", err, "correlationID", correlationID)
		} else if s3URL != "" {
			log.Infow("Response uploaded to S3", "s3_url", s3URL, "correlationID", correlationID)
			// Downstream services can read this URL via ((ServiceName.s3_url)) in request body/headers/URL
			esaLog.Response.Body = map[string]interface{}{
				"s3_url":               s3URL,
				"uploaded_at":          time.Now().UTC().Format(time.RFC3339),
				"correlation_id":       correlationID,
				"original_status_code": statusCode,
			}
		}
	}

	return nil
}

// getServiceTimeout determines the timeout to use for a service call
func (si *ServiceInvoker) getServiceTimeout(esaLog *models.EsaLog) int {
	// First, check if the service has a specific timeout configured
	if timeoutValue, exists := esaLog.ExtraFields["timeout"]; exists {
		if timeout, ok := timeoutValue.(int); ok && timeout > 0 {
			return timeout
		}
	}

	// If service timeout is not configured or invalid, fall back to global configuration
	config := commoninit.GetConfig()
	if config != nil {
		if globalTimeout := config.GetInt("RestExecuteTimeoutInSeconds"); globalTimeout > 0 {
			return globalTimeout
		}
	}

	// Final fallback to default timeout (30 seconds)
	return 30
}

// prepareRequestBody processes the service request body, URL, and headers by replacing placeholders with actual values.
// additionalConfig may be nil; placeholder_cleanup is read when non-nil.
// ctx carries the request-scoped deadline so that resolving a large/pathological field aborts at the
// overall request budget rather than spinning; pass context.Background() when no deadline applies.
func (si *ServiceInvoker) prepareRequestBody(ctx context.Context, esaLog *models.EsaLog, masterDTO *common_dto.MasterDTO, additionalConfig *AdditionalConfig) {
	if additionalConfig == nil {
		additionalConfig = &AdditionalConfig{}
	}

	// Cache for resolved placeholders
	placeholderCache := make(map[string]string)

	// Build a map of services by name for faster lookup
	serviceNameMap := make(map[string]*models.EsaLog)
	for _, svc := range masterDTO.EsaServices {
		if svc.ServiceId != esaLog.ServiceId && shouldIncludeForServicePlaceholderResolution(svc) {
			serviceNameMap[svc.ServiceName] = svc
		}
	}
	addVarPseudoService(serviceNameMap, masterDTO)

	var bodyCleanup map[string]PlaceholderCleanupFieldConfig
	if additionalConfig.PlaceholderCleanup != nil && additionalConfig.PlaceholderCleanup.RequestBody != nil {
		bodyCleanup = additionalConfig.PlaceholderCleanup.RequestBody
	}

	// Process request body (per top-level key only for placeholder_cleanup)
	if esaLog.Request.Body != nil {
		si.processMapPlaceholders(ctx, esaLog.Request.Body, masterDTO, serviceNameMap, placeholderCache, bodyCleanup)
	}

	// Process headers
	if esaLog.Request.Headers != nil {
		for key, value := range esaLog.Request.Headers {
			// Skip token placeholders - they will be handled by token management
			if strings.Contains(value, "{{TOKEN_PLACEHOLDER}}") {
				continue
			}

			var opts *PlaceholderCleanupOptions
			if additionalConfig.PlaceholderCleanup != nil && additionalConfig.PlaceholderCleanup.Headers != nil {
				if fc, ok := additionalConfig.PlaceholderCleanup.Headers[key]; ok {
					opts = fieldConfigToCleanupOptions(&fc)
				}
			}
			processedValue := si.expressionProcessor.ProcessPlaceholdersCtx(ctx, value, masterDTO, serviceNameMap, placeholderCache, opts)
			// Trim whitespace and newlines from header values
			processedValue = strings.TrimSpace(processedValue)
			processedValue = strings.ReplaceAll(processedValue, "\n", "")
			processedValue = strings.ReplaceAll(processedValue, "\r", "")
			esaLog.Request.Headers[key] = processedValue
		}
	}

	// Process URL
	if esaLog.Request.URL != "" {
		var urlOpts *PlaceholderCleanupOptions
		if additionalConfig.PlaceholderCleanup != nil && additionalConfig.PlaceholderCleanup.URL != nil {
			urlOpts = fieldConfigToCleanupOptions(additionalConfig.PlaceholderCleanup.URL)
		}
		esaLog.Request.URL = si.expressionProcessor.ProcessPlaceholdersCtx(ctx, esaLog.Request.URL, masterDTO, serviceNameMap, placeholderCache, urlOpts)
	}
}

// processMapPlaceholders recursively processes placeholders in a map structure.
// requestBodyCleanup applies only to direct string children of this map (top-level body keys when called from prepareRequestBody); nested maps use nil.
func (si *ServiceInvoker) processMapPlaceholders(ctx context.Context, data map[string]interface{}, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog, placeholderCache map[string]string, requestBodyCleanup map[string]PlaceholderCleanupFieldConfig) {
	for key, value := range data {
		switch v := value.(type) {
		case string:
			var opts *PlaceholderCleanupOptions
			if requestBodyCleanup != nil {
				if fc, ok := requestBodyCleanup[key]; ok {
					opts = fieldConfigToCleanupOptions(&fc)
				}
			}
			result := si.expressionProcessor.ProcessPlaceholdersWithTypeCtx(ctx, v, TypeMode("typed"), masterDTO, serviceNameMap, placeholderCache, opts)
			data[key] = result
		case map[string]interface{}:
			si.processMapPlaceholders(ctx, v, masterDTO, serviceNameMap, placeholderCache, nil)
		case []interface{}:
			si.processArrayPlaceholders(ctx, v, masterDTO, serviceNameMap, placeholderCache)
		}
	}
}

// PrepareRequestForFanOutCall prepares request for one fan-out call: runs normal prepareRequestBody then overlays item_binding with item context.
// Each call works on a deep copy of the request body so concurrent fan-out goroutines never share mutable body state.
// additionalConfig propagates placeholder_cleanup the same way as prepareRequestBody.
func (si *ServiceInvoker) PrepareRequestForFanOutCall(ctx context.Context, esaLog *models.EsaLog, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog, item map[string]interface{}, itemBinding FanOutItemBinding, additionalConfig *AdditionalConfig) {
	if additionalConfig == nil {
		additionalConfig = &AdditionalConfig{}
	}
	if esaLog != nil && esaLog.Request.Body != nil {
		esaLog.Request.Body = deepCopyMap(esaLog.Request.Body)
	}
	si.prepareRequestBody(ctx, esaLog, masterDTO, additionalConfig)
	placeholderCache := make(map[string]string)
	for k, v := range itemBinding.RequestBody {
		if esaLog.Request.Body == nil {
			esaLog.Request.Body = make(map[string]interface{})
		}
		var opts *PlaceholderCleanupOptions
		if additionalConfig.PlaceholderCleanup != nil && additionalConfig.PlaceholderCleanup.RequestBody != nil {
			if fc, ok := additionalConfig.PlaceholderCleanup.RequestBody[k]; ok {
				opts = fieldConfigToCleanupOptions(&fc)
			}
		}
		resolved := si.expressionProcessor.ProcessPlaceholdersWithTypeCtx(ctx, v, TypeMode("typed"), masterDTO, serviceNameMap, placeholderCache, opts, item)
		esaLog.Request.Body[k] = resolved
	}
	for k, v := range itemBinding.Headers {
		if esaLog.Request.Headers == nil {
			esaLog.Request.Headers = make(map[string]string)
		}
		var opts *PlaceholderCleanupOptions
		if additionalConfig.PlaceholderCleanup != nil && additionalConfig.PlaceholderCleanup.Headers != nil {
			if fc, ok := additionalConfig.PlaceholderCleanup.Headers[k]; ok {
				opts = fieldConfigToCleanupOptions(&fc)
			}
		}
		resolved := si.expressionProcessor.ProcessPlaceholdersWithTypeCtx(ctx, v, TypeModeString, masterDTO, serviceNameMap, placeholderCache, opts, item)
		if s, ok := resolved.(string); ok {
			esaLog.Request.Headers[k] = strings.TrimSpace(s)
		}
	}
}

// ExecuteSingleFanOutCall runs one HTTP call for a fan-out (no token retry). Returns statusCode, responseBody, error.
func (si *ServiceInvoker) ExecuteSingleFanOutCall(ctx context.Context, esaLog *models.EsaLog, httpClient *http.Client, additionalConfig *AdditionalConfig) (int, string, error) {
	callStart := time.Now()
	timeout := si.getServiceTimeout(esaLog)
	statusCode, responseBody, err := si.executeHTTPRequest(ctx, esaLog, httpClient, timeout)
	serviceDurationMs := time.Since(callStart).Milliseconds()
	requestStartTime := utility.GetRequestStartTime(ctx)
	if requestStartTime.IsZero() {
		requestStartTime = callStart
	}
	elapsedSinceStartMs := time.Since(requestStartTime).Milliseconds()
	groupIndex := utility.GetGroupIndex(ctx)
	groupLabel := ""
	if groupIndex >= 0 {
		groupLabel = fmt.Sprintf("group_%d", groupIndex+1)
	}
	errorMsg := ""
	if err != nil {
		errorMsg = err.Error()
	}
	log := si.getLog(ctx)
	log.Infow("Checkpoint",
		"checkpoint", "service_invoked",
		"service_name", esaLog.ServiceName,
		"service_url", esaLog.Request.URL,
		"http_status_code", statusCode,
		"service_duration_ms", serviceDurationMs,
		"elapsed_since_start_ms", elapsedSinceStartMs,
		"group_index", groupIndex,
		"group_label", groupLabel,
		"error", errorMsg)
	return statusCode, responseBody, err
}

// processArrayPlaceholders processes placeholders in array elements
func (si *ServiceInvoker) processArrayPlaceholders(ctx context.Context, data []interface{}, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog, placeholderCache map[string]string) {
	for i, value := range data {
		switch v := value.(type) {
		case string:
			data[i] = si.expressionProcessor.ProcessPlaceholdersCtx(ctx, v, masterDTO, serviceNameMap, placeholderCache, nil)
		case map[string]interface{}:
			si.processMapPlaceholders(ctx, v, masterDTO, serviceNameMap, placeholderCache, nil)
		case []interface{}:
			// Recursively process nested arrays
			si.processArrayPlaceholders(ctx, v, masterDTO, serviceNameMap, placeholderCache)
		}
	}
}

// Helper methods

func (si *ServiceInvoker) generateCorrelationID(request *esa_request_dto.ProcessSequenceRequest) string {
	return fmt.Sprintf("%s_%s_%s", request.WorkflowID, request.SequenceID, uuid.New().String()[:8])
}

// shouldUsePreConfiguredResponse checks if service should use pre-configured response instead of making API call
func (si *ServiceInvoker) shouldUsePreConfiguredResponse(esaLog *models.EsaLog) bool {
	// Check send_response flag - if false, use pre-configured response
	if sendResponse, exists := esaLog.ExtraFields["send_response"]; exists {
		if sr, ok := sendResponse.(bool); ok && !sr {
			// Only use pre-configured response if send_response is false AND we have a valid response_body
			return si.hasValidPreConfiguredResponse(esaLog)
		}
	}

	// Check for non-null and non-empty response_body (even if send_response is true/missing)
	return si.hasValidPreConfiguredResponse(esaLog)
}

// hasValidPreConfiguredResponse checks if there's a valid non-null, non-empty response_body
func (si *ServiceInvoker) hasValidPreConfiguredResponse(esaLog *models.EsaLog) bool {
	responseBody, exists := esaLog.ExtraFields["response_body"]
	if !exists {
		return false // response_body field doesn't exist
	}

	if responseBody == nil {
		return false // response_body is null
	}

	rb, ok := responseBody.(map[string]interface{})
	if !ok {
		return false // response_body is not a valid JSON object
	}

	if len(rb) == 0 {
		return false // response_body is empty JSON object {}
	}

	return true // response_body is non-null and non-empty
}

// processPreConfiguredResponse processes a service using pre-configured response
func (si *ServiceInvoker) processPreConfiguredResponse(ctx context.Context, esaLog *models.EsaLog, correlationID string) error {
	log := si.getLog(ctx)
	log.Infow("Using pre-configured response", "serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName, "correlationID", correlationID)

	// Set start time
	startTime := time.Now()
	esaLog.StartTime = startTime

	// Get pre-configured response
	if responseBody, exists := esaLog.ExtraFields["response_body"]; exists {
		if rb, ok := responseBody.(map[string]interface{}); ok {
			esaLog.Response.Body = rb
			esaLog.Response.StatusCode = 200
			esaLog.Status = "COMPLETED"
			esaLog.EndTime = time.Now()
			esaLog.TimeTaken = float64(time.Since(startTime).Milliseconds())
			// Stamp source only when the pre-configured/static response is actually used
			// (EXECUTE path), so plug_response_into (apply_to: static_response) can wrap it.
			esaLog.Response.Source = responseSourceStaticResponse

			log.Infow("Pre-configured response applied successfully", "serviceID", esaLog.ServiceId, "correlationID", correlationID)
			return nil
		}
	}

	// If no valid pre-configured response found, mark as failed
	esaLog.Status = "FAILED"
	esaLog.EndTime = time.Now()
	esaLog.TimeTaken = float64(time.Since(startTime).Milliseconds())
	esaLog.Response.Body = map[string]interface{}{
		"error": "Pre-configured response not found or invalid",
	}
	esaLog.Response.StatusCode = 500

	return pkgerrors.New("pre-configured response not found or invalid")
}

// processTokenConfigPlaceholders returns the token configuration as-is.
//
// NOTE: Token configuration does NOT support placeholder or variable resolution. Values such as
// token_endpoint, cache_key, and the token request headers/body are used verbatim; ((var.NAME)),
// ((ServiceName.path)), <Object.Field>, and {{...}} expressions are NOT resolved here. If dynamic
// token configuration is ever required, resolution can be added, but for now this is intentionally
// a no-op to avoid altering existing token-management behaviour.
func (si *ServiceInvoker) processTokenConfigPlaceholders(config *token_service.TokenConfig) *token_service.TokenConfig {
	// Create a copy to avoid modifying the original
	processedConfig := *config

	// Placeholder/variable resolution is intentionally not performed for token config.

	return &processedConfig
}

func (si *ServiceInvoker) processS3ConfigPlaceholders(config *s3_service.S3Config, masterDTO *common_dto.MasterDTO, esaLog *models.EsaLog) *s3_service.S3Config {
	if config == nil {
		return nil
	}
	processedConfig := *config
	processedConfig.Metadata = copyMetadata(config.Metadata)

	placeholderCache := make(map[string]string)
	serviceNameMap := make(map[string]*models.EsaLog)
	if masterDTO != nil {
		for _, svc := range masterDTO.EsaServices {
			if svc.ServiceId != esaLog.ServiceId && shouldIncludeForServicePlaceholderResolution(svc) {
				serviceNameMap[svc.ServiceName] = svc
			}
		}
		addVarPseudoService(serviceNameMap, masterDTO)
	}

	resolve := func(s string) string {
		if s == "" {
			return ""
		}
		return si.expressionProcessor.ProcessPlaceholders(s, masterDTO, serviceNameMap, placeholderCache, nil)
	}

	processedConfig.KeyPrefix = resolve(config.KeyPrefix)
	processedConfig.FileName = resolve(config.FileName)
	processedConfig.ContentType = resolve(config.ContentType)
	processedConfig.ACL = resolve(config.ACL)
	processedConfig.BucketName = resolve(config.BucketName)
	processedConfig.Region = resolve(config.Region)
	processedConfig.AWSAccessKey = resolve(config.AWSAccessKey)
	processedConfig.AWSSecretKey = resolve(config.AWSSecretKey)
	for k, v := range processedConfig.Metadata {
		if str, ok := v.(string); ok {
			processedConfig.Metadata[k] = resolve(str)
		}
	}
	return &processedConfig
}

func copyMetadata(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (si *ServiceInvoker) injectTokenIntoHeaders(esaLog *models.EsaLog, token string) {
	if esaLog.Request.Headers == nil {
		esaLog.Request.Headers = make(map[string]string)
	}

	// Look for {{TOKEN_PLACEHOLDER}} in headers and replace with actual token
	for key, value := range esaLog.Request.Headers {
		if strings.Contains(value, "{{TOKEN_PLACEHOLDER}}") {
			// Replace only the placeholder, preserving any prefix/suffix (like "Bearer ")
			esaLog.Request.Headers[key] = strings.ReplaceAll(value, "{{TOKEN_PLACEHOLDER}}", token)
		}
	}
}
