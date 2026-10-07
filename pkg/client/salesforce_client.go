package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"esa/internal/app/constants"
	"esa/internal/app/db/repository"
	commoninit "esa/internal/app/init"
	"esa/internal/app/models/esa_models"
	"esa/internal/app/service/cache_service"
	"esa/internal/app/utility"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/dmi-infotech/common-modules/go/contracts"
	pkgerrors "github.com/pkg/errors"
)

// Constants for Salesforce composite API limits
const (
	maxCompositeSubrequests         = 25 // Salesforce limit for subrequests per composite request
	maxQuerySubrequestsPerComposite = 5  // Salesforce limit: only up to 5 subrequests can be query operations
)

// Singleton variables for SalesforceClient
var (
	salesforceClient ISalesforceClient
	salesforceOnce   sync.Once
)

// SalesforceService manages the Salesforce client
type SalesforceService struct{}

// InitializeSalesforceClient initializes the Salesforce client
func (s *SalesforceService) InitializeSalesforceClient(db *sql.DB) {
	salesforceOnce.Do(func() {
		// Use the global repository that was initialized in main.go
		salesforceClient = NewSalesforceClient()
	})
}

// GetSalesforceClient returns the Salesforce client instance
func (s *SalesforceClient) GetSalesforceClient() ISalesforceClient {
	// Ensure the client is initialized before returning it
	if salesforceClient == nil {
		log := commoninit.GetLogger()
		log.Warnw("Salesforce client was accessed before initialization, initializing now")
		// Initialize the client if needed
		return s
	}
	return salesforceClient
}

type ISalesforceClient interface {
	QuerySalesforceObjects(ctx context.Context, refID string, objectsWithFields map[string][]string) (map[string]map[string]interface{}, error)
	QuerySalesforceObjectsWithConditions(ctx context.Context, refID string, objectsWithFields map[string][]string, objectsWithConditions map[string]bool) (map[string]interface{}, error)
}

type SalesforceClient struct {
	logger            contracts.Logger
	apiClient         contracts.APIClient
	queryObjectRepo   repository.IQueryObjectRepository
	cacheService      *cache_service.CacheService
	accessToken       string
	tokenExpiry       time.Time
	tokenRefreshMutex sync.Mutex
	// Optimization: Pre-compiled regex patterns
	dependencyRegex *regexp.Regexp
	regexOnce       sync.Once
	httpClient      *http.Client
	mu              sync.Mutex
	tokenTimestamp  time.Time
	// MaxChunkParallel caps concurrent SF composite chunk goroutines per level (0 = no cap)
	MaxChunkParallel int
}

type SalesforceAuthResponse struct {
	AccessToken string `json:"access_token"`
	InstanceURL string `json:"instance_url"`
	TokenType   string `json:"token_type"`
	IssuedAt    string `json:"issued_at"`
	Signature   string `json:"signature"`
	ExpiresIn   int    `json:"expires_in"`
}

type CompositeRequest struct {
	CompositeRequest []SubRequest `json:"compositeRequest"`
}

type SubRequest struct {
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	ReferenceID string            `json:"referenceId"`
	Body        map[string]string `json:"body,omitempty"`
}

type CompositeResponse struct {
	CompositeResponse []SubResponse `json:"compositeResponse"`
}

type SubResponse struct {
	Body            interface{}       `json:"body"`
	HTTPHeaders     map[string]string `json:"httpHeaders"`
	HTTPStatusCode  int               `json:"httpStatusCode"`
	ReferenceID     string            `json:"referenceId"`
	ReferenceIDType string            `json:"referenceIdType"`
}

type SalesforceQueryResponse struct {
	Done           bool                     `json:"done"`
	Records        []map[string]interface{} `json:"records"`
	TotalSize      int                      `json:"totalSize"`
	NextRecordsURL string                   `json:"nextRecordsUrl,omitempty"`
}

// NewSalesforceClient creates a new Salesforce client instance
func NewSalesforceClient() *SalesforceClient {
	log := commoninit.GetLogger()

	// Log the Salesforce configuration (without sensitive values)
	log.Infow("Initializing Salesforce client",
		"loginURL", commoninit.GetConfigString("salesforce.loginurl", ""),
		"baseURL", commoninit.GetConfigString("salesforce.baseurl", ""),
		"hasUsername", commoninit.GetConfigString("salesforce.username", "") != "",
		"hasPassword", commoninit.GetConfigString("salesforce.password", "") != "",
		"hasSecurityToken", commoninit.GetConfigString("salesforce.securitytoken", "") != "",
		"hasClientID", commoninit.GetConfigString("salesforce.clientid", "") != "",
		"hasClientSecret", commoninit.GetConfigString("salesforce.clientsecret", "") != "")

	log.Info("Config values being used",
		"loginURL", commoninit.GetConfigString("salesforce.loginurl", ""),
		"baseURL", commoninit.GetConfigString("salesforce.baseurl", ""),
		"hasUsername", commoninit.GetConfigString("salesforce.username", ""),
		"hasPassword", commoninit.GetConfigString("salesforce.password", "") != "",
		"hasSecurityToken", commoninit.GetConfigString("salesforce.securitytoken", "") != "",
		"hasClientID", commoninit.GetConfigString("salesforce.clientid", ""),
		"hasClientSecret", commoninit.GetConfigString("salesforce.clientsecret", "") != "")

	client := &SalesforceClient{
		logger:           commoninit.GetLogger(),
		apiClient:        commoninit.GetAPIClient(),
		MaxChunkParallel: commoninit.GetConfigInt(constants.ServerMaxSFChunkParallel, constants.DefaultMaxSFChunkParallel),
		// Don't try to get the repository immediately - it might not be initialized yet
		// We'll get it lazily when needed
		// CacheService will be set later via SetCacheService method
	}

	// Pre-compile regex for better performance
	client.regexOnce.Do(func() {
		client.dependencyRegex = regexp.MustCompile(`<([^.>]+)\.([^>]+)>`)
	})

	// Try to get an access token, but don't fail initialization if it doesn't work
	ctx := context.Background()
	_, err := client.getAccessToken(ctx)
	if err != nil {
		log.Warnw("Failed to initialize Salesforce access token, will retry on first API call", "error", err)
	} else {
		log.Infow("Successfully initialized Salesforce client with valid access token")
	}

	return client
}

// SetCacheService sets the cache service for the Salesforce client
func (c *SalesforceClient) SetCacheService(cacheService *cache_service.CacheService) {
	c.cacheService = cacheService
}

// getLog returns the request-scoped logger when set in context, otherwise c.logger.WithContext(ctx).
func (c *SalesforceClient) getLog(ctx context.Context) contracts.Logger {
	if reqLog := utility.GetRequestLogger(ctx); reqLog != nil {
		return reqLog
	}
	return c.logger.WithContext(ctx)
}

// objectsWithFields example: {"Lead": ["Id", "FirstName", "LastName"], "Contact": ["Id", "Email"]}
func (c *SalesforceClient) QuerySalesforceObjects(ctx context.Context, refID string, objectsWithFields map[string][]string) (map[string]map[string]interface{}, error) {
	log := c.getLog(ctx)

	// Initialize the repository if not already done
	if c.queryObjectRepo == nil {
		// Try to get the repository, but don't fail if it's not available
		repo := repository.GetQueryObjectRepository()
		if repo == nil {
			log.Warnw("QueryObjectRepository not initialized - will continue with limited functionality")
			// Return empty but valid result to avoid crashes
			return make(map[string]map[string]interface{}), nil
		}
		c.queryObjectRepo = repo
	}

	accessToken, err := c.getAccessToken(ctx)
	if err != nil {
		log.Errorw("Failed to get Salesforce access token", "error", err)
		return nil, err
	}

	result := make(map[string]map[string]interface{})

	compositeRequest, err := c.buildCompositeRequest(ctx, refID, objectsWithFields)
	if err != nil {
		log.Errorw("Failed to build Salesforce composite request", "error", err)
		return nil, err
	}

	sfBaseURL := commoninit.GetConfigString("salesforce.baseurl", "")
	compositeURL := fmt.Sprintf("%s/services/data/v55.0/composite", sfBaseURL)

	httpRequest, err := c.apiClient.CreateJSONRequest(ctx, http.MethodPost, compositeURL, accessToken, compositeRequest)
	if err != nil {
		log.Errorw("Failed to create Salesforce API request", "error", err)
		return nil, err
	}

	timeout := commoninit.GetConfigInt("RestExecuteTimeoutInSeconds", 30)
	maxConnectionRetries := commoninit.GetConfigInt(constants.HttpConnectionRetryCountKey, constants.DefaultConnectionRetryCount)
	statusCode, responseBody, err := c.apiClient.RestExecuteWithConnectionRetry(ctx, httpRequest, timeout, maxConnectionRetries)
	if err != nil {
		log.Errorw("Failed to execute Salesforce API request", "error", err, "statusCode", statusCode)
		return nil, err
	}

	// Handling of unauthorized error (token expiry)
	if statusCode == http.StatusUnauthorized {
		log.Infow("Salesforce token expired, refreshing and retrying request")

		// Force token refresh (guarded so concurrent requests don't stampede the auth endpoint)
		c.invalidateToken(accessToken)
		accessToken, err = c.getAccessToken(ctx)
		if err != nil {
			log.Errorw("Failed to refresh Salesforce access token", "error", err)
			return nil, err
		}

		// Retry request with new token
		httpRequest, err = c.apiClient.CreateJSONRequest(ctx, http.MethodPost, compositeURL, accessToken, compositeRequest)
		if err != nil {
			log.Errorw("Failed to create Salesforce API request after token refresh", "error", err)
			return nil, err
		}

		statusCode, responseBody, err = c.apiClient.RestExecuteWithConnectionRetry(ctx, httpRequest, timeout, maxConnectionRetries)
		if err != nil {
			log.Errorw("Failed to execute Salesforce API request after token refresh", "error", err, "statusCode", statusCode)
			return nil, err
		}

		if statusCode != http.StatusOK {
			log.Errorw("Salesforce API request failed after token refresh", "statusCode", statusCode, "response", responseBody)
			return nil, fmt.Errorf("salesforce API request failed with status code %d after token refresh: %s", statusCode, responseBody)
		}
	} else if statusCode != http.StatusOK {
		log.Errorw("Salesforce API request failed", "statusCode", statusCode, "response", responseBody)
		return nil, fmt.Errorf("salesforce API request failed with status code %d: %s", statusCode, responseBody)
	}

	var compositeResponse CompositeResponse
	if err := json.Unmarshal([]byte(responseBody), &compositeResponse); err != nil {
		log.Errorw("Failed to parse Salesforce composite response", "error", err)
		return nil, err
	}

	// Process each subresponse
	for _, subResponse := range compositeResponse.CompositeResponse {
		if subResponse.HTTPStatusCode != http.StatusOK {
			// Extract error details from the response body if possible
			var errorDetails interface{}
			if subResponse.Body != nil {
				errorDetails = subResponse.Body
			}
			log.Warnw("Subrequest failed",
				"referenceId", subResponse.ReferenceID,
				"statusCode", subResponse.HTTPStatusCode,
				"errorDetails", errorDetails)
			continue
		}

		// Extract object name by removing "_query" suffix
		objectName := strings.TrimSuffix(subResponse.ReferenceID, "_query")

		var queryResponse SalesforceQueryResponse
		bodyBytes, err := json.Marshal(subResponse.Body)
		if err != nil {
			log.Errorw("Failed to marshal response body", "error", err, "object", objectName)
			continue
		}

		if err := json.Unmarshal(bodyBytes, &queryResponse); err != nil {
			log.Errorw("Failed to parse query response", "error", err, "object", objectName)
			continue
		}

		if queryResponse.TotalSize > 0 {
			result[objectName] = queryResponse.Records[0]
		} else {
			result[objectName] = make(map[string]interface{})
		}
	}

	return result, nil
}

// Enhanced method that supports conditional queries
func (c *SalesforceClient) QuerySalesforceObjectsWithConditions(ctx context.Context, refID string, objectsWithFields map[string][]string, objectsWithConditions map[string]bool) (map[string]interface{}, error) {
	log := c.getLog(ctx)

	// Initialize the repository if not already done
	if c.queryObjectRepo == nil {
		// Try to get the repository, but don't fail if it's not available
		repo := repository.GetQueryObjectRepository()
		if repo == nil {
			log.Warnw("QueryObjectRepository not initialized - will continue with limited functionality")
			// Return empty but valid result to avoid crashes
			return make(map[string]interface{}), nil
		}
		c.queryObjectRepo = repo
	}

	if len(objectsWithFields) == 0 {
		log.Infow("No objects to fetch from Salesforce")
		return make(map[string]interface{}), nil
	}

	accessToken, err := c.getAccessToken(ctx)
	if err != nil {
		log.Errorw("Failed to get Salesforce access token", "error", err)
		return nil, err
	}

	result := make(map[string]interface{})

	// Separate labeled (lab_ prefixed) keys from regular object keys
	regularObjectsWithFields := make(map[string][]string)
	labeledObjectsWithFields := make(map[string][]string) // key still has "lab_" prefix
	var labelNames []string                               // label names without "lab_" prefix

	for key, fields := range objectsWithFields {
		if strings.HasPrefix(key, "lab_") {
			labeledObjectsWithFields[key] = fields
			labelName := strings.TrimPrefix(key, "lab_")
			labelNames = append(labelNames, labelName)
		} else {
			regularObjectsWithFields[key] = fields
		}
	}

	if len(labeledObjectsWithFields) > 0 {
		log.Infow("Detected labeled query references", "labelCount", len(labelNames), "labels", labelNames)
	}

	// Fetch query objects and run dependency analysis (needed for level-based batching)
	normalizedObjectNames := make([]string, 0, len(regularObjectsWithFields))
	for objectName := range regularObjectsWithFields {
		normalizedObjectNames = append(normalizedObjectNames, objectName)
	}
	var queryObjects map[string]*esa_models.QueryObjectRelationshipMap
	if c.cacheService != nil {
		var err error
		queryObjects, err = c.cacheService.GetQueryObjectRelationships(ctx, normalizedObjectNames)
		if err != nil {
			log.Warnw("Cache get failed for query objects", "error", err.Error())
		}
	}
	if queryObjects == nil && c.queryObjectRepo != nil {
		var err error
		queryObjects, err = c.queryObjectRepo.FindByObjectNames(ctx, normalizedObjectNames)
		if err != nil {
			log.Errorw("Failed to fetch query object relationships", "error", err)
			return nil, pkgerrors.Wrap(err, "failed to fetch query object relationships")
		}
	}
	if queryObjects == nil {
		log.Errorw("Query objects unavailable - cannot build composite request")
		return nil, pkgerrors.New("QueryObjectRepository not initialized or fetch failed")
	}

	// Merge labeled query configs (keyed by lab_<label>) into queryObjects so labels participate in
	// the SAME dependency graph and composite batching as regular objects. This lets a query reference
	// a label and a label reference a query/label in ANY direction, resolved by level ordering.
	if len(labelNames) > 0 {
		labelConfigs, lErr := c.getLabelConfigs(ctx, labelNames)
		if lErr != nil {
			log.Errorw("Failed to fetch label configs; labeled queries will be skipped", "error", lErr)
		} else if len(labelConfigs) > 0 {
			merged := make(map[string]*esa_models.QueryObjectRelationshipMap, len(queryObjects)+len(labelConfigs))
			for k, v := range queryObjects {
				merged[k] = v
			}
			for name, cfg := range labelConfigs {
				merged["lab_"+strings.ToLower(name)] = cfg
			}
			queryObjects = merged
		}
	}

	// Pass the full objectsWithFields (including lab_ keys) so labeled queries are analyzed, ordered,
	// and batched alongside regular objects.
	enhancedObjectsWithFields, objectDependencies, err := c.optimizedDependencyAnalysis(ctx, objectsWithFields, queryObjects)
	if err != nil {
		log.Errorw("Failed to perform dependency analysis", "error", err)
		return nil, err
	}

	// Fetch query objects for dependency objects (e.g. Lead) that were added by dependency analysis
	// but were not in the original request, so we can build their SOQL and run them in level 0.
	queryObjects = c.ensureQueryObjectsForDependencies(ctx, enhancedObjectsWithFields, queryObjects)

	orderedObjectNames := c.optimizedDetermineQueryOrder(enhancedObjectsWithFields, objectDependencies)
	objectDependsOn := c.objectDependenciesWithinSet(queryObjects, orderedObjectNames)
	levels := c.computeDependencyLevels(orderedObjectNames, objectDependsOn)

	// Group object names by level (0 = no deps, 1 = deps on level 0, etc.)
	levelToObjects := make(map[int][]string)
	for _, name := range orderedObjectNames {
		l := levels[name]
		levelToObjects[l] = append(levelToObjects[l], name)
	}
	maxLevel := -1
	for l := range levelToObjects {
		if l > maxLevel {
			maxLevel = l
		}
	}

	successfulResults := make(map[string]interface{})
	for level := 0; level <= maxLevel; level++ {
		namesAtLevel := levelToObjects[level]
		if len(namesAtLevel) == 0 {
			continue
		}
		chunks := chunkBySize(namesAtLevel, maxQuerySubrequestsPerComposite)
		log.Infow("Executing composite batch", "level", level, "objectCount", len(namesAtLevel), "chunkCount", len(chunks))

		var wg sync.WaitGroup
		type chunkResult struct {
			result  map[string]interface{}
			skipped []string // objects skipped because dependency field was missing (e.g. null, omitted by SF)
			err     error
		}
		chunkResults := make([]chunkResult, len(chunks))
		maxChunkParallel := c.MaxChunkParallel
		if maxChunkParallel < 1 {
			maxChunkParallel = 0
		}
		var chunkSem chan struct{}
		if maxChunkParallel > 0 {
			chunkSem = make(chan struct{}, maxChunkParallel)
		}
		for i, chunk := range chunks {
			wg.Add(1)
			go func(idx int, chunkNames []string) {
				defer wg.Done()
				if chunkSem != nil {
					chunkSem <- struct{}{}
					defer func() { <-chunkSem }()
				}
				var res chunkResult
				res.result = make(map[string]interface{})
				compReq, skipped, buildErr := c.buildCompositeRequestForChunk(ctx, refID, chunkNames, enhancedObjectsWithFields, queryObjects, successfulResults)
				if buildErr != nil {
					chunkResults[idx] = chunkResult{result: nil, err: buildErr}
					return
				}
				res.skipped = skipped
				// If all objects in chunk were skipped (no subrequests), bind {} for each and we're done.
				if len(compReq.CompositeRequest) == 0 {
					chunkResults[idx] = res
					return
				}
				statusCode, responseBody, execErr := c.executeCompositeRequest(ctx, compReq, accessToken)
				if execErr != nil {
					chunkResults[idx] = chunkResult{result: nil, err: execErr}
					return
				}
				if statusCode != http.StatusOK {
					chunkResults[idx] = chunkResult{result: nil, err: fmt.Errorf("salesforce API returned status %d: %s", statusCode, responseBody)}
					return
				}
				var compositeResponse CompositeResponse
				if jsonErr := json.Unmarshal([]byte(responseBody), &compositeResponse); jsonErr != nil {
					chunkResults[idx] = chunkResult{result: nil, err: jsonErr}
					return
				}
				res.result, _ = c.processCompositeResponse(ctx, compositeResponse, objectsWithConditions)
				chunkResults[idx] = res
			}(i, chunk)
		}
		wg.Wait()

		for i, cr := range chunkResults {
			if cr.err != nil {
				log.Errorw("Chunk failed", "level", level, "chunkIndex", i, "error", cr.err)
				continue
			}
			for k, v := range cr.result {
				successfulResults[k] = v
			}
			for _, name := range cr.skipped {
				successfulResults[name] = map[string]interface{}{}
			}
		}
	}

	for k, v := range successfulResults {
		result[k] = v
	}

	return result, nil
}

// navigateValue walks the given segments through maps, single records, and {"records":[...]} / array shapes.
func navigateValue(data interface{}, segments []string) interface{} {
	current := data
	for _, seg := range segments {
		if current == nil {
			return nil
		}
		switch v := current.(type) {
		case map[string]interface{}:
			found := false
			for k, val := range v {
				if strings.EqualFold(k, seg) {
					current = val
					found = true
					break
				}
			}
			if !found {
				// Try a wrapped records array (first record)
				if recs, ok := v["records"].([]interface{}); ok && len(recs) > 0 {
					if rec, ok := recs[0].(map[string]interface{}); ok {
						for k, val := range rec {
							if strings.EqualFold(k, seg) {
								current = val
								found = true
								break
							}
						}
					}
				}
			}
			if !found {
				return nil
			}
		case []interface{}:
			if len(v) == 0 {
				return nil
			}
			rec, ok := v[0].(map[string]interface{})
			if !ok {
				return nil
			}
			found := false
			for k, val := range rec {
				if strings.EqualFold(k, seg) {
					current = val
					found = true
					break
				}
			}
			if !found {
				return nil
			}
		default:
			return nil
		}
	}
	return current
}

// getLabelConfigs fetches label configs (keyed by lowercase label name) via the cache service when
// available, falling back to the repository — mirroring how regular object relationships are fetched.
func (c *SalesforceClient) getLabelConfigs(ctx context.Context, labelNames []string) (map[string]*esa_models.QueryObjectRelationshipMap, error) {
	if c.cacheService != nil {
		cfgs, err := c.cacheService.GetQueryObjectRelationshipsByLabels(ctx, labelNames)
		if err != nil {
			c.getLog(ctx).Warnw("Cache get failed for label configs, falling back to repository", "error", err.Error())
		} else if cfgs != nil {
			return cfgs, nil
		}
	}
	if c.queryObjectRepo != nil {
		return c.queryObjectRepo.FindByLabels(ctx, labelNames)
	}
	return nil, pkgerrors.New("QueryObjectRepository not initialized for label lookup")
}

// isLimitError checks if a subresponse indicates a Salesforce query limit error
func (c *SalesforceClient) isLimitError(subResponse SubResponse) bool {
	if subResponse.HTTPStatusCode != http.StatusBadRequest {
		return false
	}

	if subResponse.Body == nil {
		return false
	}

	// Marshal body to string for checking
	bodyBytes, err := json.Marshal(subResponse.Body)
	if err != nil {
		return false
	}

	bodyStr := strings.ToLower(string(bodyBytes))
	return strings.Contains(bodyStr, "processing_halted") ||
		strings.Contains(bodyStr, "limit number of query") ||
		strings.Contains(bodyStr, "limit number of collections")
}

// processCompositeResponse processes composite response and returns successful results and failed queries
func (c *SalesforceClient) processCompositeResponse(ctx context.Context, compositeResponse CompositeResponse, objectsWithConditions map[string]bool) (map[string]interface{}, []string) {
	log := c.getLog(ctx)
	result := make(map[string]interface{})
	var failedQueries []string

	for _, subResponse := range compositeResponse.CompositeResponse {
		// Extract object name by removing "_query" suffix
		objectName := strings.TrimSuffix(subResponse.ReferenceID, "_query")

		if subResponse.HTTPStatusCode != http.StatusOK {
			// Check if it's a limit error
			if c.isLimitError(subResponse) {
				failedQueries = append(failedQueries, objectName)
				log.Warnw("Query hit Salesforce limit, will retry separately",
					"object", objectName,
					"referenceId", subResponse.ReferenceID,
					"statusCode", subResponse.HTTPStatusCode)
				continue
			}

			// Extract error details from the response body if possible
			var errorDetails interface{}
			if subResponse.Body != nil {
				errorDetails = subResponse.Body
			}
			log.Warnw("Subrequest failed",
				"referenceId", subResponse.ReferenceID,
				"statusCode", subResponse.HTTPStatusCode,
				"errorDetails", errorDetails)
			continue
		}

		// Process successful response
		var queryResponse SalesforceQueryResponse
		bodyBytes, err := json.Marshal(subResponse.Body)
		if err != nil {
			log.Errorw("Failed to marshal response body", "error", err, "object", objectName)
			continue
		}

		if err := json.Unmarshal(bodyBytes, &queryResponse); err != nil {
			log.Errorw("Failed to parse query response", "error", err, "object", objectName)
			continue
		}

		log.Infow("Salesforce query response", "object", objectName, "totalSize", queryResponse.TotalSize, "recordCount", len(queryResponse.Records))

		// Enhanced logic: return direct object for single records, array for multiple/conditional
		hasConditions := objectsWithConditions[objectName]

		if queryResponse.TotalSize > 0 {
			if hasConditions || queryResponse.TotalSize > 1 {
				// Return full array of records for conditional filtering or multiple records
				records := make([]interface{}, len(queryResponse.Records))
				for i, record := range queryResponse.Records {
					// Store raw record - normalization happens at access time
					records[i] = record
				}
				result[objectName] = records
				log.Infow("Returning full array for conditional object", "object", objectName, "recordCount", len(records))
			} else {
				// Return single record (raw) for non-conditional single objects
				result[objectName] = queryResponse.Records[0]
				log.Infow("Returning single record for non-conditional object", "object", objectName)
			}
		} else {
			// No records found
			if hasConditions {
				result[objectName] = []interface{}{}
			} else {
				result[objectName] = make(map[string]interface{})
			}
			log.Infow("No records found", "object", objectName, "hasConditions", hasConditions)
		}
	}

	return result, failedQueries
}

// executeCompositeRequest executes a composite request with token refresh handling
func (c *SalesforceClient) executeCompositeRequest(ctx context.Context, compositeRequest *CompositeRequest, accessToken string) (int, string, error) {
	log := c.getLog(ctx)
	sfBaseURL := commoninit.GetConfigString("salesforce.baseurl", "")
	compositeURL := fmt.Sprintf("%s/services/data/v55.0/composite", sfBaseURL)

	httpRequest, err := c.apiClient.CreateJSONRequest(ctx, http.MethodPost, compositeURL, accessToken, compositeRequest)
	if err != nil {
		log.Errorw("Failed to create Salesforce API request", "error", err)
		return 0, "", err
	}

	timeout := commoninit.GetConfigInt("RestExecuteTimeoutInSeconds", 30)
	maxConnectionRetries := commoninit.GetConfigInt(constants.HttpConnectionRetryCountKey, constants.DefaultConnectionRetryCount)
	statusCode, responseBody, err := c.apiClient.RestExecuteWithConnectionRetry(ctx, httpRequest, timeout, maxConnectionRetries)
	if err != nil {
		log.Errorw("Failed to execute Salesforce API request", "error", err, "statusCode", statusCode)
		return statusCode, responseBody, err
	}

	// Handling of unauthorized error (token expiry)
	if statusCode == http.StatusUnauthorized {
		log.Infow("Salesforce token expired, refreshing and retrying request")

		// Force token refresh (guarded so concurrent chunks don't stampede the auth endpoint)
		c.invalidateToken(accessToken)
		accessToken, err = c.getAccessToken(ctx)
		if err != nil {
			log.Errorw("Failed to refresh Salesforce access token", "error", err)
			return statusCode, responseBody, err
		}

		// Retry request with new token
		httpRequest, err = c.apiClient.CreateJSONRequest(ctx, http.MethodPost, compositeURL, accessToken, compositeRequest)
		if err != nil {
			log.Errorw("Failed to create Salesforce API request after token refresh", "error", err)
			return statusCode, responseBody, err
		}

		statusCode, responseBody, err = c.apiClient.RestExecuteWithConnectionRetry(ctx, httpRequest, timeout, maxConnectionRetries)
		if err != nil {
			log.Errorw("Failed to execute Salesforce API request after token refresh", "error", err, "statusCode", statusCode)
			return statusCode, responseBody, err
		}
	}

	return statusCode, responseBody, nil
}

// buildSOQLQueryWithValueReplacement builds SOQL query by replacing composite references with actual values.
// allReplaced is false when a referenced field was missing from the dependency result (SF often omits null
// fields from the JSON), in which case the caller should skip the query and bind {} for this object.
func (c *SalesforceClient) buildSOQLQueryWithValueReplacement(_ context.Context, objectName string, fields []string, queryObject *esa_models.QueryObjectRelationshipMap, refID string, successfulResults map[string]interface{}) (query string, allReplaced bool, err error) {
	fieldList := strings.Join(fields, ", ")
	fromObject := soqlFromObject(objectName, queryObject)
	whereClause := fmt.Sprintf("%s = %s", queryObject.QueryRelation, c.escapeSOQLValue(refID))
	query = fmt.Sprintf("SELECT %s FROM %s WHERE %s", fieldList, fromObject, whereClause)
	allReplaced = true

	// Add additional conditions if provided
	if queryObject.AdditionalConditions.Valid && queryObject.AdditionalConditions.String != "" {
		additionalConditions := normalizeSOQLWhitespace(strings.TrimSpace(queryObject.AdditionalConditions.String))

		// First convert config placeholders <Object.Field> to @{object_query.records[0].field};
		// then replace those with actual values (replaceCompositeReferencesWithValues only matches @{}).
		withCompositeRefs := c.processAdditionalConditionsReferences(additionalConditions)
		startsWithWhere := strings.HasPrefix(strings.ToUpper(additionalConditions), "WHERE")

		// Null/blank-aware condition pruning (WHERE-prefixed clauses only). Engages only when the
		// clause is WHERE-prefixed and has at least one placeholder that resolves to empty
		// (nil/blank). Applies three-valued logic: drop empty OR-branches and keep survivors; an
		// AND with an empty operand (or a single empty comparison) is unsatisfiable and skips the
		// query. When every placeholder resolves, this is not engaged and behavior is byte-identical
		// to before. Anything outside the supported grammar falls back to the existing behavior.
		if startsWithWhere {
			classify := c.makeEmptyClassifier(successfulResults)
			if hasEmptyComposite(withCompositeRefs, classify) {
				prunedClause, decision := pruneWhereConditions(withCompositeRefs, classify)
				switch decision {
				case prunedOK:
					processed := c.replaceCompositeReferencesWithValues(prunedClause, successfulResults)
					// Survivors carry only resolved placeholders, so nothing should remain; guard anyway.
					if !strings.Contains(processed, "@{") {
						query = fmt.Sprintf("SELECT %s FROM %s %s", fieldList, fromObject, processed)
						return query, true, nil
					}
					// Unexpected leftover -> skip rather than emit an unresolved query.
					return query, false, nil
				case prunedSkip:
					// No satisfiable identity filter (e.g. an all-empty OR-group, or an AND with an
					// empty operand). Skip the query and bind {} for this object.
					return query, false, nil
				case prunedFallback:
					// Out of supported grammar -> retain existing behavior (handled below).
				}
			}
		}

		processedConditions := c.replaceCompositeReferencesWithValues(withCompositeRefs, successfulResults)
		if processedConditions == "" {
			return "", false, fmt.Errorf("failed to replace composite references in additional conditions")
		}
		// SF REST API omits null-valued fields from JSON, so the dependency record may not contain the field.
		// If any placeholder was not replaced, caller will skip this query and bind {} for this object.
		allReplaced = !strings.Contains(processedConditions, "@{")

		// If additional_conditions starts with WHERE, use it as the entire WHERE clause (replace default)
		if startsWithWhere {
			query = fmt.Sprintf("SELECT %s FROM %s %s", fieldList, fromObject, processedConditions)
		} else {
			query += " " + processedConditions
		}
	}

	return query, allReplaced, nil
}

// makeEmptyClassifier returns an emptyClassifier bound to the given dependency results. A composite
// placeholder is empty when its resolved value is nil or a blank string; ok is false only when the
// placeholder is not a recognizable composite reference (which forces the pruner to fall back).
func (c *SalesforceClient) makeEmptyClassifier(successfulResults map[string]interface{}) emptyClassifier {
	return func(placeholder string) (empty bool, ok bool) {
		sub := compositeRefRegex.FindStringSubmatch(placeholder)
		if len(sub) < 3 {
			return false, false
		}
		depObjectName := strings.ToLower(sub[1])
		fieldName := strings.ToLower(sub[2])
		value := c.extractFieldValueFromResults(depObjectName, fieldName, successfulResults)
		return isBlankSOQLValue(value), true
	}
}

// isBlankSOQLValue reports whether a resolved dependency value should be treated as empty for
// null-pruning: nil, or a string that is empty/whitespace. Empty collections ([] / {}) and other
// non-string values are NOT blank — they are real, present values matched explicitly by configs.
func isBlankSOQLValue(value interface{}) bool {
	if value == nil {
		return true
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s) == ""
	}
	return false
}

// replaceCompositeReferencesWithValues replaces @{object_query.records[0].field} with actual values
func (c *SalesforceClient) replaceCompositeReferencesWithValues(additionalConditions string, successfulResults map[string]interface{}) string {
	processedConditions := compositeRefRegex.ReplaceAllStringFunc(additionalConditions, func(match string) string {
		submatch := compositeRefRegex.FindStringSubmatch(match)
		if len(submatch) >= 3 {
			depObjectName := strings.ToLower(submatch[1]) // e.g., "lead"
			fieldName := strings.ToLower(submatch[2])     // e.g., "pan__c"

			// Get the value from successful results
			value := c.extractFieldValueFromResults(depObjectName, fieldName, successfulResults)
			if value == nil {
				// Return original if value not found (will cause query to fail, but that's expected)
				return match
			}

			// Escape and format the value for SOQL
			return c.escapeSOQLValue(value)
		}
		return match
	})

	return processedConditions
}

// extractFieldValueFromResults extracts field value from successful query results.
// Object key lookup is case-insensitive so that "lead" finds results stored under "Lead".
// fieldName may be a dotted/nested path; it is resolved via navigateValue, which also handles
// single records, arrays, and {"records":[...]} shapes.
func (c *SalesforceClient) extractFieldValueFromResults(objectName string, fieldName string, successfulResults map[string]interface{}) interface{} {
	objectData, exists := successfulResults[objectName]
	if !exists {
		// Case-insensitive lookup: successfulResults may be keyed by "Lead" while we look up "lead"
		for k, v := range successfulResults {
			if strings.EqualFold(k, objectName) {
				objectData = v
				exists = true
				break
			}
		}
	}
	if !exists {
		return nil
	}

	return navigateValue(objectData, strings.Split(fieldName, "."))
}

// escapeSOQLValue safely escapes and formats a value for SOQL query
func (c *SalesforceClient) escapeSOQLValue(value interface{}) string {
	if value == nil {
		return "null"
	}

	switch v := value.(type) {
	case string:
		// Escape backslashes first, then single quotes, then wrap in quotes (SOQL string literal rules)
		escaped := strings.ReplaceAll(v, "\\", "\\\\")
		escaped = strings.ReplaceAll(escaped, "'", "\\'")
		return fmt.Sprintf("'%s'", escaped)
	case bool:
		return fmt.Sprintf("%t", v)
	case int, int8, int16, int32, int64:
		return fmt.Sprintf("%d", v)
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", v)
	case float32, float64:
		return fmt.Sprintf("%g", v)
	default:
		// Convert to string and escape (backslash first, then single quote)
		str := fmt.Sprintf("%v", v)
		escaped := strings.ReplaceAll(str, "\\", "\\\\")
		escaped = strings.ReplaceAll(escaped, "'", "\\'")
		return fmt.Sprintf("'%s'", escaped)
	}
}

func (c *SalesforceClient) buildCompositeRequest(ctx context.Context, refID string, objectsWithFields map[string][]string) (*CompositeRequest, error) {
	log := c.getLog(ctx)

	compositeRequest := &CompositeRequest{
		CompositeRequest: []SubRequest{},
	}

	// Check if repository is available
	if c.queryObjectRepo == nil {
		log.Warnw("QueryObjectRepository not initialized - cannot build proper composite request")
		return nil, pkgerrors.New("QueryObjectRepository not initialized")
	}

	// Object names are already normalized by field extraction, so we can use them directly
	normalizedObjectNames := make([]string, 0, len(objectsWithFields))
	for objectName := range objectsWithFields {
		normalizedObjectNames = append(normalizedObjectNames, objectName)
	}

	// Fetch query relationships using normalized names
	var queryObjects map[string]*esa_models.QueryObjectRelationshipMap
	var err error

	if c.cacheService != nil {
		log.Debugw("Attempting to fetch query objects from cache", "objectNames", normalizedObjectNames)
		queryObjects, err = c.cacheService.GetQueryObjectRelationships(ctx, normalizedObjectNames)
		if err != nil {
			log.Warnw("Cache get failed, fetching from database", "error", err.Error())
		}
	}

	if queryObjects == nil && c.queryObjectRepo != nil {
		log.Debugw("Fetching query objects from database", "objectNames", normalizedObjectNames)
		queryObjects, err = c.queryObjectRepo.FindByObjectNames(ctx, normalizedObjectNames)
		if err != nil {
			log.Errorw("Failed to fetch query object relationships", "error", err)
			return nil, pkgerrors.Wrap(err, "failed to fetch query object relationships")
		}

		// Cache the results if cache service is available
		if c.cacheService != nil && len(queryObjects) > 0 {
			// Note: Cache setting will be handled by the cache service internally
			log.Debugw("Query objects fetched from database", "count", len(queryObjects))
		}
	}

	log.Infow("Successfully fetched query object relationships", "requested_count", len(normalizedObjectNames), "found_count", len(queryObjects))

	// Perform dependency analysis to ensure proper query ordering
	enhancedObjectsWithFields, objectDependencies, err := c.optimizedDependencyAnalysis(ctx, objectsWithFields, queryObjects)
	if err != nil {
		log.Errorw("Failed to perform dependency analysis", "error", err)
		return nil, err
	}

	// Determine optimal query order based on dependencies
	orderedObjectNames := c.optimizedDetermineQueryOrder(enhancedObjectsWithFields, objectDependencies)
	log.Infow("Determined query order", "order", orderedObjectNames, "dependencies", objectDependencies)

	// Process each object in dependency order
	for _, objectName := range orderedObjectNames {
		queryObject, exists := queryObjects[objectName]
		if !exists || queryObject == nil {
			log.Warnw("Query object not found, skipping", "object", objectName)
			continue
		}

		fields := enhancedObjectsWithFields[objectName]
		if len(fields) == 0 {
			log.Warnw("No fields found for object, skipping", "object", objectName)
			continue
		}

		// Build SOQL query
		query, err := c.buildSOQLQuery(ctx, objectName, fields, queryObjects[objectName], refID)
		if err != nil {
			log.Errorw("Failed to build SOQL query", "object", objectName, "error", err)
			continue
		}

		// Create sub-request
		subRequest := SubRequest{
			Method:      "GET",
			URL:         fmt.Sprintf("/services/data/v55.0/query/?q=%s", url.QueryEscape(query)),
			ReferenceID: fmt.Sprintf("%s_query", objectName),
		}

		compositeRequest.CompositeRequest = append(compositeRequest.CompositeRequest, subRequest)
	}

	return compositeRequest, nil
}

// buildSOQLQuery builds a SOQL query for the composite API request. It keeps placeholders
// as composite references (e.g. @{lead_query.records[0].id}) so Salesforce can resolve them
// when executing the composite request. Use buildSOQLQueryWithValueReplacement when you
// have actual values from prior sub-request results and need a concrete query string.
func (c *SalesforceClient) buildSOQLQuery(_ context.Context, objectName string, fields []string, queryObject *esa_models.QueryObjectRelationshipMap, refID string) (string, error) {
	fieldList := strings.Join(fields, ", ")
	fromObject := soqlFromObject(objectName, queryObject)
	whereClause := fmt.Sprintf("%s = %s", queryObject.QueryRelation, c.escapeSOQLValue(refID))
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s", fieldList, fromObject, whereClause)

	// Add additional conditions if provided
	if queryObject.AdditionalConditions.Valid && queryObject.AdditionalConditions.String != "" {
		// Trim whitespace, normalize Unicode spaces (e.g. non-breaking) to ASCII, and ensure proper formatting
		additionalConditions := normalizeSOQLWhitespace(strings.TrimSpace(queryObject.AdditionalConditions.String))

		// Replace placeholders with composite request references
		// Pattern: <ObjectName.FieldName> -> @{ObjectName_query.records[0].FieldName}
		processedConditions := c.processAdditionalConditionsReferences(additionalConditions)

		// If additional_conditions starts with WHERE, use it as the entire WHERE clause (replace default)
		if strings.HasPrefix(strings.ToUpper(additionalConditions), "WHERE") {
			query = fmt.Sprintf("SELECT %s FROM %s %s", fieldList, fromObject, processedConditions)
		} else {
			// Add the processed additional conditions to the query
			query += " " + processedConditions
		}
	}

	return query, nil
}

// soqlFromObject returns the SOQL FROM target for a node. For labeled nodes (lab_ prefix) the real
// Salesforce object is the label config's query_object; for regular nodes the node name is the object.
func soqlFromObject(objectName string, queryObject *esa_models.QueryObjectRelationshipMap) string {
	if queryObject != nil && strings.HasPrefix(objectName, "lab_") && strings.TrimSpace(queryObject.QueryObject) != "" {
		return queryObject.QueryObject
	}
	return objectName
}

// normalizeSOQLWhitespace replaces Unicode space characters (e.g. non-breaking space U+00A0) with
// ASCII space so SOQL does not get "unexpected token: ' '" from the Salesforce parser.
func normalizeSOQLWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, s)
}

// processAdditionalConditionsReferences converts placeholder references to composite request references
func (c *SalesforceClient) processAdditionalConditionsReferences(additionalConditions string) string {
	// Use pre-compiled regex for better performance
	processedConditions := c.dependencyRegex.ReplaceAllStringFunc(additionalConditions, func(match string) string {
		submatch := c.dependencyRegex.FindStringSubmatch(match)
		if len(submatch) >= 3 {
			objectName := strings.ToLower(submatch[1]) // Normalize to lowercase: "Lead" -> "lead"
			fieldName := strings.ToLower(submatch[2])  // Normalize to lowercase: "PAN__c" -> "pan__c"

			// Convert to composite reference format with lowercase
			// @{lead_query.records[0].pan__c}
			return fmt.Sprintf("@{%s_query.records[0].%s}", objectName, fieldName)
		}
		return match // return original if parsing fails
	})

	return processedConditions
}

// optimizedDependencyAnalysis performs optimized single-pass dependency analysis
func (c *SalesforceClient) optimizedDependencyAnalysis(ctx context.Context, objectsWithFields map[string][]string, queryObjects map[string]*esa_models.QueryObjectRelationshipMap) (map[string][]string, map[string]map[string]struct{}, error) {
	log := c.getLog(ctx)

	// Pre-allocate maps with estimated capacity
	enhancedObjectsWithFields := make(map[string][]string, len(objectsWithFields)*2)
	additionalDependencies := make(map[string]map[string]struct{}) // object -> set of fields

	// Copy original objects first
	for obj, fields := range objectsWithFields {
		enhancedObjectsWithFields[obj] = make([]string, len(fields))
		copy(enhancedObjectsWithFields[obj], fields)
	}

	// Single pass through all objects to collect dependencies and additional fields
	for objectName := range objectsWithFields {
		queryObject, exists := queryObjects[objectName]
		if !exists || queryObject == nil {
			continue
		}

		// Process AdditionalFields from database configuration
		if queryObject.AdditionalFields.Valid && queryObject.AdditionalFields.String != "" {
			additionalFields := strings.Split(queryObject.AdditionalFields.String, ",")
			for _, field := range additionalFields {
				field = strings.TrimSpace(field)
				if field != "" {
					// Normalize field casing to ensure consistent deduplication with extracted fields
					normalizedField := strings.ToLower(field)

					// Add to existing object fields using optimized merge
					if existingFields, exists := enhancedObjectsWithFields[objectName]; exists {
						enhancedObjectsWithFields[objectName] = c.optimizedMergeFields(existingFields, []string{normalizedField})
					} else {
						enhancedObjectsWithFields[objectName] = []string{normalizedField}
					}
					log.Infow("Added field from additional_fields configuration",
						"object", objectName,
						"field", normalizedField)
				}
			}
		}

		// Process AdditionalConditions for dependency analysis
		if queryObject.AdditionalConditions.Valid && queryObject.AdditionalConditions.String != "" {
			// Use pre-compiled regex for better performance
			matches := c.dependencyRegex.FindAllStringSubmatch(queryObject.AdditionalConditions.String, -1)

			for _, match := range matches {
				if len(match) >= 3 {
					depObject := match[1] // e.g., "Lead"
					depField := match[2]  // e.g., "PAN__c"

					// Use set-based operations for O(1) field deduplication
					if fieldSet, exists := additionalDependencies[depObject]; exists {
						fieldSet[depField] = struct{}{}
					} else {
						additionalDependencies[depObject] = map[string]struct{}{depField: {}}
					}
				}
			}
		}
	}

	// Convert sets back to slices and merge with enhanced objects
	for depObj, fieldSet := range additionalDependencies {
		depFields := make([]string, 0, len(fieldSet))
		for field := range fieldSet {
			depFields = append(depFields, field)
		}

		if existingFields, exists := enhancedObjectsWithFields[depObj]; exists {
			// Use optimized field merging
			enhancedObjectsWithFields[depObj] = c.optimizedMergeFields(existingFields, depFields)
		} else {
			// Add new dependency object
			enhancedObjectsWithFields[depObj] = depFields
			log.Infow("Added dependent object from additional_conditions",
				"dependentObject", depObj,
				"requiredFields", depFields)
		}
	}

	return enhancedObjectsWithFields, additionalDependencies, nil
}

// optimizedMergeFields uses set-based operations for O(1) deduplication.
// Deduplication is case-insensitive so that "Dealer_Code__c" and "dealer_code__c"
// are treated as the same field (Salesforce rejects duplicate field in SELECT).
func (c *SalesforceClient) optimizedMergeFields(existing []string, new []string) []string {
	// Key by lowercase for case-insensitive dedup; value keeps first-seen casing for SOQL
	fieldByLower := make(map[string]string, len(existing)+len(new))

	for _, field := range existing {
		lower := strings.ToLower(field)
		if _, seen := fieldByLower[lower]; !seen {
			fieldByLower[lower] = field
		}
	}
	for _, field := range new {
		lower := strings.ToLower(field)
		if _, seen := fieldByLower[lower]; !seen {
			fieldByLower[lower] = field
		}
	}

	result := make([]string, 0, len(fieldByLower))
	for _, field := range fieldByLower {
		result = append(result, field)
	}
	return result
}

// optimizedDetermineQueryOrder uses dependency graph for optimal ordering
func (c *SalesforceClient) optimizedDetermineQueryOrder(allObjects map[string][]string, dependencies map[string]map[string]struct{}) []string {
	// Convert dependencies map to simple object set for faster lookup
	dependencyObjects := make(map[string]struct{}, len(dependencies))
	for depObject := range dependencies {
		dependencyObjects[depObject] = struct{}{}
	}

	// Pre-allocate result slice with known capacity
	result := make([]string, 0, len(allObjects))

	// Add dependency objects first
	for objectName := range allObjects {
		if _, isDependency := dependencyObjects[objectName]; isDependency {
			result = append(result, objectName)
		}
	}

	// Add remaining objects
	for objectName := range allObjects {
		if _, isDependency := dependencyObjects[objectName]; !isDependency {
			result = append(result, objectName)
		}
	}

	return result
}

// ensureQueryObjectsForDependencies fetches query object config for any objects in enhancedObjectsWithFields
// that are not yet in queryObjects (e.g. dependency objects like Lead added by additional_conditions).
// Returns a combined map so that level-0 composites can include and run those queries.
func (c *SalesforceClient) ensureQueryObjectsForDependencies(ctx context.Context, enhancedObjectsWithFields map[string][]string, queryObjects map[string]*esa_models.QueryObjectRelationshipMap) map[string]*esa_models.QueryObjectRelationshipMap {
	var missing []string       // regular object dependency nodes
	var missingLabels []string // label dependency nodes (lab_ prefix), label name without prefix
	for obj := range enhancedObjectsWithFields {
		if getQueryObjectCaseInsensitive(queryObjects, obj) != nil {
			continue
		}
		if strings.HasPrefix(obj, "lab_") {
			missingLabels = append(missingLabels, strings.TrimPrefix(obj, "lab_"))
		} else {
			missing = append(missing, obj)
		}
	}
	if len(missing) == 0 && len(missingLabels) == 0 {
		return queryObjects
	}
	if c.logger != nil {
		log := c.getLog(ctx)
		log.Infow("Fetching query objects for dependency nodes", "objects", missing, "labels", missingLabels)
	}

	var additional map[string]*esa_models.QueryObjectRelationshipMap
	if len(missing) > 0 {
		if c.cacheService != nil {
			var err error
			additional, err = c.cacheService.GetQueryObjectRelationships(ctx, missing)
			if err != nil && c.logger != nil {
				c.getLog(ctx).Warnw("Cache get failed for dependency objects", "error", err.Error())
			}
		}
		if additional == nil && c.queryObjectRepo != nil {
			var err error
			additional, err = c.queryObjectRepo.FindByObjectNames(ctx, missing)
			if err != nil {
				if c.logger != nil {
					c.getLog(ctx).Warnw("Failed to fetch query objects for dependency objects", "error", err)
				}
				additional = nil
			}
		}
	}

	// Fetch any label dependency nodes (a regular/labeled query whose conditions reference <lab_x.field>
	// where lab_x was not explicitly requested). Merged under the lab_ key with the label's config.
	var additionalLabels map[string]*esa_models.QueryObjectRelationshipMap
	if len(missingLabels) > 0 {
		labelConfigs, err := c.getLabelConfigs(ctx, missingLabels)
		if err != nil {
			if c.logger != nil {
				c.getLog(ctx).Warnw("Failed to fetch label configs for dependency labels", "error", err)
			}
		} else if len(labelConfigs) > 0 {
			additionalLabels = make(map[string]*esa_models.QueryObjectRelationshipMap, len(labelConfigs))
			for name, cfg := range labelConfigs {
				additionalLabels["lab_"+strings.ToLower(name)] = cfg
			}
		}
	}

	if len(additional) == 0 && len(additionalLabels) == 0 {
		return queryObjects
	}

	// Merge into a new map so we don't mutate the possibly-cached queryObjects
	combined := make(map[string]*esa_models.QueryObjectRelationshipMap, len(queryObjects)+len(additional)+len(additionalLabels))
	for k, v := range queryObjects {
		combined[k] = v
	}
	for k, v := range additional {
		combined[k] = v
	}
	for k, v := range additionalLabels {
		combined[k] = v
	}
	return combined
}

// getQueryObjectCaseInsensitive returns the query object for objectName. The repo keys the map by
// lowercase query_object, so "Lead" must find the entry keyed "lead".
func getQueryObjectCaseInsensitive(queryObjects map[string]*esa_models.QueryObjectRelationshipMap, objectName string) *esa_models.QueryObjectRelationshipMap {
	if qo, ok := queryObjects[objectName]; ok && qo != nil {
		return qo
	}
	for k, v := range queryObjects {
		if strings.EqualFold(k, objectName) && v != nil {
			return v
		}
	}
	return nil
}

// objectDependenciesWithinSet returns for each object the list of other objects (from the same set)
// it references in additional_conditions (e.g. <Lead.Field>). Keys are matched case-insensitively.
func (c *SalesforceClient) objectDependenciesWithinSet(queryObjects map[string]*esa_models.QueryObjectRelationshipMap, objectNames []string) map[string][]string {
	setLower := make(map[string]string, len(objectNames))
	for _, n := range objectNames {
		setLower[strings.ToLower(n)] = n
	}
	out := make(map[string][]string)
	for _, objectName := range objectNames {
		qo := getQueryObjectCaseInsensitive(queryObjects, objectName)
		if qo == nil || !qo.AdditionalConditions.Valid || qo.AdditionalConditions.String == "" {
			continue
		}
		matches := c.dependencyRegex.FindAllStringSubmatch(qo.AdditionalConditions.String, -1)
		seen := make(map[string]struct{})
		for _, m := range matches {
			if len(m) < 3 {
				continue
			}
			depLower := strings.ToLower(m[1])
			if ref, inSet := setLower[depLower]; inSet && ref != objectName {
				if _, ok := seen[ref]; !ok {
					seen[ref] = struct{}{}
					out[objectName] = append(out[objectName], ref)
				}
			}
		}
	}
	return out
}

// computeDependencyLevels returns level per object: 0 = no deps in set, 1 = deps only level 0, etc.
func (c *SalesforceClient) computeDependencyLevels(objectNames []string, objectDependsOn map[string][]string) map[string]int {
	levels := make(map[string]int)
	for _, n := range objectNames {
		levels[n] = 0
	}
	for iter := 0; iter < len(objectNames); iter++ {
		updated := false
		for _, obj := range objectNames {
			deps := objectDependsOn[obj]
			maxDepLevel := -1
			for _, d := range deps {
				if l, ok := levels[d]; ok && l > maxDepLevel {
					maxDepLevel = l
				}
			}
			newLevel := 0
			if maxDepLevel >= 0 {
				newLevel = maxDepLevel + 1
			}
			if newLevel != levels[obj] {
				levels[obj] = newLevel
				updated = true
			}
		}
		if !updated {
			break
		}
	}
	return levels
}

// buildCompositeRequestForChunk builds a single composite request for the given object names (max 5 for query limit).
// If successfulResults is non-nil, SOQL is built with value replacement for composite refs. When a referenced
// field is missing from the dependency result (e.g. null and omitted by SF), the object is skipped and added to
// skipped so the caller can bind {} for it.
func (c *SalesforceClient) buildCompositeRequestForChunk(ctx context.Context, refID string, chunkNames []string, objectsWithFields map[string][]string, queryObjects map[string]*esa_models.QueryObjectRelationshipMap, successfulResults map[string]interface{}) (*CompositeRequest, []string, error) {
	chunkMap := make(map[string][]string, len(chunkNames))
	for _, n := range chunkNames {
		if f, ok := objectsWithFields[n]; ok {
			chunkMap[n] = f
		}
	}
	if len(chunkMap) == 0 {
		return &CompositeRequest{CompositeRequest: []SubRequest{}}, nil, nil
	}
	req := &CompositeRequest{CompositeRequest: make([]SubRequest, 0, len(chunkMap))}
	var skipped []string
	for _, objectName := range chunkNames {
		fields, ok := chunkMap[objectName]
		if !ok || len(fields) == 0 {
			continue
		}
		queryObject := getQueryObjectCaseInsensitive(queryObjects, objectName)
		if queryObject == nil {
			continue
		}
		var query string
		var err error
		if successfulResults != nil {
			var allReplaced bool
			query, allReplaced, err = c.buildSOQLQueryWithValueReplacement(ctx, objectName, fields, queryObject, refID, successfulResults)
			if err != nil {
				return nil, nil, err
			}
			if !allReplaced {
				// Dependency was queried but referenced field missing (e.g. null and omitted by SF). Skip query and bind {}.
				skipped = append(skipped, objectName)
				continue
			}
		} else {
			query, err = c.buildSOQLQuery(ctx, objectName, fields, queryObject, refID)
			if err != nil {
				return nil, nil, err
			}
		}
		req.CompositeRequest = append(req.CompositeRequest, SubRequest{
			Method:      "GET",
			URL:         fmt.Sprintf("/services/data/v55.0/query/?q=%s", url.QueryEscape(query)),
			ReferenceID: fmt.Sprintf("%s_query", objectName),
		})
	}
	return req, skipped, nil
}

// chunkBySize splits a slice into chunks of at most size.
func chunkBySize(names []string, size int) [][]string {
	if size <= 0 {
		return [][]string{names}
	}
	var chunks [][]string
	for i := 0; i < len(names); i += size {
		end := i + size
		if end > len(names) {
			end = len(names)
		}
		chunks = append(chunks, names[i:end])
	}
	return chunks
}

// invalidateToken forces a token refresh under the same mutex that getAccessToken uses, but only
// if the currently cached token still matches staleToken. Composite chunks run concurrently, so a
// single expired token can produce many simultaneous 401s; without this guard each goroutine would
// blank the expiry (an unsynchronized write — a data race) and trigger its own redundant login,
// hammering the Salesforce auth endpoint. By comparing against the stale token, only the first 401
// forces one refresh; the rest observe the already-refreshed token and reuse it.
func (c *SalesforceClient) invalidateToken(staleToken string) {
	c.tokenRefreshMutex.Lock()
	defer c.tokenRefreshMutex.Unlock()
	if c.accessToken == staleToken {
		c.tokenExpiry = time.Time{}
	}
}

func (c *SalesforceClient) getAccessToken(ctx context.Context) (string, error) {
	log := c.getLog(ctx)

	// Use a mutex to prevent concurrent token refreshes
	c.tokenRefreshMutex.Lock()
	defer c.tokenRefreshMutex.Unlock()

	// Check if valid token
	if c.accessToken != "" && time.Now().Before(c.tokenExpiry) {
		return c.accessToken, nil
	}

	log.Infow("Obtaining new Salesforce access token")

	sfBaseURL := commoninit.GetConfigString("salesforce.baseurl", "")
	sfLoginURL := commoninit.GetConfigString("salesforce.loginurl", "")
	if sfBaseURL == "" || sfLoginURL == "" {
		return "", fmt.Errorf("salesforce URLs not configured")
	}

	tokenURL := sfLoginURL + "/services/oauth2/token"

	// Check if all required credentials are configured
	username := commoninit.GetConfigString("salesforce.username", "")
	password := commoninit.GetConfigString("salesforce.password", "")
	securityToken := commoninit.GetConfigString("salesforce.securitytoken", "")
	clientID := commoninit.GetConfigString("salesforce.clientid", "")
	clientSecret := commoninit.GetConfigString("salesforce.clientsecret", "")

	if username == "" || password == "" || securityToken == "" || clientID == "" || clientSecret == "" {
		return "", fmt.Errorf("missing required Salesforce credentials in configuration")
	}

	formData := url.Values{}
	formData.Set("grant_type", "password")
	formData.Set("client_id", clientID)
	formData.Set("client_secret", clientSecret)
	formData.Set("username", username)
	formData.Set("password", password+securityToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(formData.Encode()))
	if err != nil {
		log.Errorw("Failed to create auth request", "error", err)
		return "", err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Errorw("Failed to execute auth request", "error", err)
		return "", err
	}
	defer resp.Body.Close()

	// Read response body into a variable
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Errorw("Failed to read response body", "error", err)
		return "", err
	}
	bodyString := string(bodyBytes)

	// Log response headers for debugging
	log.Infow("Auth response headers",
		"statusCode", resp.StatusCode,
		"contentType", resp.Header.Get("Content-Type"),
		"contentLength", resp.Header.Get("Content-Length"))

	if resp.StatusCode != http.StatusOK {
		log.Errorw("Auth request failed", "statusCode", resp.StatusCode, "response", bodyString)
		return "", fmt.Errorf("auth request failed with status code %d", resp.StatusCode)
	}

	// Check for HTML response (which would cause JSON parsing to fail)
	if strings.HasPrefix(strings.TrimSpace(bodyString), "<") {
		// Log the first 200 chars of the response to see what we're getting
		previewLength := 200
		if len(bodyString) < previewLength {
			previewLength = len(bodyString)
		}
		responsePreview := bodyString[:previewLength]

		log.Errorw("Received HTML instead of JSON from Salesforce",
			"contentType", resp.Header.Get("Content-Type"),
			"responsePreview", responsePreview,
			"loginURL", sfLoginURL)

		return "", fmt.Errorf("received HTML instead of JSON from Salesforce - check credentials and endpoint URL")
	}

	// Log successful response without trying to log resp.Body directly
	log.Infow("Auth request successful", "statusCode", resp.StatusCode)

	// Use the already read body for JSON decoding
	var authResponse SalesforceAuthResponse
	if err := json.Unmarshal(bodyBytes, &authResponse); err != nil {
		// If unmarshal fails, log more context about the response
		log.Errorw("Failed to parse auth response",
			"error", err,
			"contentType", resp.Header.Get("Content-Type"),
			"responseStart", bodyString[:min(100, len(bodyString))])
		return "", err
	}

	// token expiry (default = 1 hour)
	expiresIn := authResponse.ExpiresIn
	if expiresIn == 0 {
		expiresIn = 3600 // Default to 1 hour
	}

	// Set 5 minutes buffer before actual expiry
	buffer := 5 * time.Minute

	c.accessToken = authResponse.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(expiresIn)*time.Second - buffer)

	log.Infow("Successfully obtained Salesforce access token",
		"expiresAt", c.tokenExpiry,
		"instanceURL", authResponse.InstanceURL,
		"tokenType", authResponse.TokenType)

	return c.accessToken, nil
}

// Helper function to find minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
