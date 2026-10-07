package token_service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	commoninit "esa/internal/app/init"
	"esa/internal/app/utility"
	"esa/internal/app/constants"

	"github.com/dmi-infotech/common-modules/go/contracts"
	pkgerrors "github.com/pkg/errors"
)

// TokenConfig represents token management configuration
type TokenConfig struct {
	Enabled               bool         `json:"enabled"`
	TokenEndpoint         string       `json:"token_endpoint"`
	TokenRequest          TokenRequest `json:"token_request"`
	TokenPath             string       `json:"token_path"`
	ExpiresInPath         string       `json:"expires_in_path,omitempty"`
	CacheKey              string       `json:"cache_key"`
	RetryOn401            bool         `json:"retry_on_401"`
	MaxTokenRetries       int          `json:"max_token_retries"`
	TokenRefreshBuffer    int          `json:"token_refresh_buffer,omitempty"`
	CheckExpiryOnResponse bool         `json:"check_expiry_on_response"`
	HasExpiryField        bool         `json:"has_expiry_field"`
}

// TokenRequest represents the token request structure
type TokenRequest struct {
	Method  string                 `json:"method"`
	Headers map[string]interface{} `json:"headers"`
	Body    map[string]interface{} `json:"body"`
}

// TokenResponse represents the token response from cache or API
type TokenResponse struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
	TokenType   string    `json:"token_type"`
}

// TokenService handles token management operations
type TokenService struct {
	logger    contracts.Logger
	apiClient contracts.APIClient
	cache     contracts.CacheProvider
}

// NewTokenService creates a new token service
func NewTokenService() *TokenService {
	return &TokenService{
		logger:    commoninit.GetLogger(),
		apiClient: commoninit.GetAPIClient(),
		cache:     commoninit.GetCache(),
	}
}

// getLog returns the request-scoped logger when set in context, otherwise ts.logger.WithContext(ctx).
func (ts *TokenService) getLog(ctx context.Context) contracts.Logger {
	if reqLog := utility.GetRequestLogger(ctx); reqLog != nil {
		return reqLog
	}
	return ts.logger.WithContext(ctx)
}

// GetValidToken gets a valid token, refreshing if necessary
func (ts *TokenService) GetValidToken(ctx context.Context, config *TokenConfig) (string, error) {
	log := ts.getLog(ctx)

	if !config.Enabled {
		return "", nil
	}

	// Try to get token from cache first
	cachedToken, err := ts.getTokenFromCache(ctx, config.CacheKey)
	if err == nil && cachedToken != nil {
		// Check if token is still valid (considering refresh buffer)
		if ts.isTokenValid(cachedToken, config.TokenRefreshBuffer) {
			log.Debugw("Using cached token", "cache_key", config.CacheKey)
			return cachedToken.AccessToken, nil
		}
		log.Debugw("Cached token expired or near expiry, refreshing", "cache_key", config.CacheKey)
	}

	// Generate new token
	return ts.generateNewToken(ctx, config)
}

// RefreshTokenOn401 handles token refresh when receiving 401 unauthorized
func (ts *TokenService) RefreshTokenOn401(ctx context.Context, config *TokenConfig) (string, error) {
	log := ts.getLog(ctx)

	if !config.Enabled || !config.RetryOn401 {
		return "", nil
	}

	log.Infow("Refreshing token due to 401 response", "cache_key", config.CacheKey)

	// Clear existing token from cache
	ts.clearTokenFromCache(ctx, config.CacheKey)

	// Generate new token
	return ts.generateNewToken(ctx, config)
}

// generateNewToken generates a new token from the auth endpoint
func (ts *TokenService) generateNewToken(ctx context.Context, config *TokenConfig) (string, error) {
	log := ts.getLog(ctx)

	log.Infow("Generating new token", "endpoint", config.TokenEndpoint)

	// Create HTTP request for token
	httpRequest, err := ts.apiClient.CreateRequestWithContentType(
		ctx,
		config.TokenRequest.Method,
		config.TokenEndpoint,
		ts.convertHeaders(config.TokenRequest.Headers),
		config.TokenRequest.Body,
	)
	if err != nil {
		log.Errorw("Failed to create token request", "error", err)
		return "", pkgerrors.Wrap(err, "failed to create token request")
	}

	// Log token request details (excluding sensitive data)
	log.Infow("Token API request",
		"method", config.TokenRequest.Method,
		"endpoint", config.TokenEndpoint,
		"headers", ts.sanitizeHeaders(config.TokenRequest.Headers),
		"cache_key", config.CacheKey)

	// Execute token request with timing (retry only on connection errors, not on timeout)
	startTime := time.Now()
	const tokenTimeoutSeconds = 30
	maxConnectionRetries := commoninit.GetConfigInt(constants.HttpConnectionRetryCountKey, constants.DefaultConnectionRetryCount)
	statusCode, responseBody, err := ts.apiClient.RestExecuteWithConnectionRetry(ctx, httpRequest, tokenTimeoutSeconds, maxConnectionRetries)
	executionTime := time.Since(startTime)

	if err != nil {
		log.Errorw("Token request failed",
			"error", err,
			"status_code", statusCode,
			"response", responseBody,
			"execution_time_ms", executionTime.Milliseconds(),
			"endpoint", config.TokenEndpoint)
		return "", pkgerrors.Wrap(err, "token request failed")
	}

	// Log token response details
	log.Infow("Token API response",
		"status_code", statusCode,
		"execution_time_ms", executionTime.Milliseconds(),
		"cache_key", config.CacheKey,
		"response_length", len(responseBody))

	if statusCode != 200 {
		log.Errorw("Token request returned non-200 status",
			"status_code", statusCode,
			"response", responseBody,
			"execution_time_ms", executionTime.Milliseconds())
		return "", fmt.Errorf("token request failed with status %d: %s", statusCode, responseBody)
	}

	// Parse token response
	var tokenData map[string]interface{}
	if err := json.Unmarshal([]byte(responseBody), &tokenData); err != nil {
		log.Errorw("Failed to parse token response", "error", err, "response", responseBody)
		return "", pkgerrors.Wrap(err, "failed to parse token response")
	}

	// Extract access token
	accessToken, err := ts.extractValueFromResponse(tokenData, config.TokenPath)
	if err != nil {
		log.Errorw("Failed to extract access token", "error", err, "token_path", config.TokenPath)
		return "", pkgerrors.Wrap(err, "failed to extract access token")
	}

	// Create token response
	tokenResponse := &TokenResponse{
		AccessToken: accessToken,
		TokenType:   "Bearer",
	}

	// Extract expiry if available
	if config.HasExpiryField && config.ExpiresInPath != "" {
		if expiresIn, err := ts.extractValueFromResponse(tokenData, config.ExpiresInPath); err == nil {
			if expiresInInt, err := strconv.Atoi(expiresIn); err == nil {
				tokenResponse.ExpiresAt = time.Now().Add(time.Duration(expiresInInt) * time.Second)
			}
		}
	}
	// If no expiry field is configured or available, token will not expire from cache perspective
	// It will rely on 401 responses from the API to trigger refresh

	// Cache the token
	if err := ts.cacheToken(ctx, config.CacheKey, tokenResponse); err != nil {
		log.Warnw("Failed to cache token", "error", err, "cache_key", config.CacheKey)
		// Continue even if caching fails
	}

	log.Infow("Successfully generated new token",
		"cache_key", config.CacheKey,
		"expires_at", tokenResponse.ExpiresAt)

	return tokenResponse.AccessToken, nil
}

// getTokenFromCache retrieves token from cache
func (ts *TokenService) getTokenFromCache(ctx context.Context, cacheKey string) (*TokenResponse, error) {
	if ts.cache == nil {
		return nil, fmt.Errorf("cache not available")
	}

	cachedData, err := ts.cache.Get(ctx, cacheKey)
	if err != nil {
		return nil, err
	}

	var tokenResponse TokenResponse
	if err := json.Unmarshal([]byte(cachedData), &tokenResponse); err != nil {
		return nil, err
	}

	return &tokenResponse, nil
}

// cacheToken stores token in cache
func (ts *TokenService) cacheToken(ctx context.Context, cacheKey string, token *TokenResponse) error {
	if ts.cache == nil {
		return fmt.Errorf("cache not available")
	}

	tokenData, err := json.Marshal(token)
	if err != nil {
		return err
	}

	// Calculate TTL until expiry
	var ttl time.Duration
	if !token.ExpiresAt.IsZero() {
		ttl = time.Until(token.ExpiresAt)
		if ttl <= 0 {
			ttl = 5 * time.Minute // Minimum TTL if token is already expired
		}
	} else {
		// For tokens without expiry, use a very long TTL (24 hours)
		// Token will be refreshed only on 401 responses
		ttl = 24 * time.Hour
	}

	return ts.cache.Set(ctx, cacheKey, string(tokenData), ttl)
}

// clearTokenFromCache removes token from cache
func (ts *TokenService) clearTokenFromCache(ctx context.Context, cacheKey string) {
	if ts.cache != nil {
		ts.cache.Delete(ctx, cacheKey)
	}
}

// isTokenValid checks if token is still valid considering refresh buffer
func (ts *TokenService) isTokenValid(token *TokenResponse, refreshBuffer int) bool {
	if token.ExpiresAt.IsZero() {
		return true // No expiry set, assume valid
	}

	// Add buffer time to check for refresh before actual expiry
	bufferDuration := time.Duration(refreshBuffer) * time.Second
	return time.Now().Add(bufferDuration).Before(token.ExpiresAt)
}

// extractValueFromResponse extracts value from nested JSON response using dot notation
func (ts *TokenService) extractValueFromResponse(data map[string]interface{}, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}

	// Simple path extraction (not supporting complex nested paths for now)
	if value, exists := data[path]; exists {
		if strValue, ok := value.(string); ok {
			return strValue, nil
		}
		// Try to convert to string if not already string
		return fmt.Sprintf("%v", value), nil
	}

	return "", fmt.Errorf("path %s not found in response", path)
}

// convertHeaders converts map[string]interface{} to map[string]string
func (ts *TokenService) convertHeaders(headers map[string]interface{}) map[string]string {
	result := make(map[string]string)
	for key, value := range headers {
		if strValue, ok := value.(string); ok {
			result[key] = strValue
		} else {
			result[key] = fmt.Sprintf("%v", value)
		}
	}
	return result
}

// sanitizeHeaders removes sensitive headers from logging
func (ts *TokenService) sanitizeHeaders(headers map[string]interface{}) map[string]interface{} {
	sanitized := make(map[string]interface{})
	for k, v := range headers {
		if k == "Authorization" {
			sanitized[k] = "***"
		} else {
			sanitized[k] = v
		}
	}
	return sanitized
}

// ShouldRefreshToken checks if token should be refreshed based on response status
func (ts *TokenService) ShouldRefreshToken(config *TokenConfig, statusCode int) bool {
	return config != nil && config.Enabled && config.RetryOn401 && statusCode == 401
}
