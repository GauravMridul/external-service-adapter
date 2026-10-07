package cache_service

import (
	"context"
	"encoding/json"
	"errors"
	"esa/internal/app/db/repository"
	"esa/internal/app/dto/common_dto"
	commoninit "esa/internal/app/init"
	"esa/internal/app/models/esa_models"
	"fmt"
	"strings"
	"sync"

	"github.com/dmi-infotech/common-modules/go/contracts"
	rediscache "github.com/dmi-infotech/common-modules/go/infrastructure/cache"
	"github.com/redis/go-redis/v9"
)

// ErrCacheDisabled is returned when cache.enabled=false is set explicitly in config.
var ErrCacheDisabled = fmt.Errorf("cache is disabled by configuration")

// ErrCacheUnavailable is returned when cache.enabled=true in config but the
// Redis connection could not be established at startup.
var ErrCacheUnavailable = fmt.Errorf("cache is unavailable: Redis connection failed at startup")

// CacheService handles caching operations for ESA
type CacheService struct {
	mu              sync.Mutex
	mgmtErr         error // set once at startup; non-nil means management endpoints are inoperable
	cache           contracts.CacheProvider
	keyBuilder      *CacheKeyBuilder
	cacheConfig     *common_dto.ESACacheConfig
	logger          contracts.Logger
	serviceRepo     repository.IServiceConfigurationRepository
	queryObjectRepo repository.IQueryObjectRepository
	redisClient     redis.UniversalClient
	scanCount       int64
	deleteBatchLen  int
}

type ClearCacheRequest struct {
	ClearAll      bool
	Match         string
	DryRun        bool
	LimitMatchKey int
}

type ClearCacheResult struct {
	Mode        string   `json:"mode"`
	Pattern     string   `json:"pattern"`
	DryRun      bool     `json:"dry_run"`
	KeysMatched int      `json:"keys_matched"`
	KeysDeleted int64    `json:"keys_deleted"`
	ScanCycles  int      `json:"scan_cycles"`
	Keys        []string `json:"keys"`
}

type ICacheService interface {
	PingCache(ctx context.Context) error
	GetCacheStats(ctx context.Context) (map[string]interface{}, error)
	GetValueByKey(ctx context.Context, key string) (string, error)
	ClearCache(ctx context.Context, req ClearCacheRequest) (*ClearCacheResult, error)
	Close(ctx context.Context) error
}

func appendLimitedKeys(existing []string, incoming []string, limit int) []string {
	if limit <= 0 {
		return append(existing, incoming...)
	}
	remaining := limit - len(existing)
	if remaining <= 0 {
		return existing
	}
	if len(incoming) <= remaining {
		return append(existing, incoming...)
	}
	return append(existing, incoming[:remaining]...)
}

const (
	defaultScanCount   = int64(500)
	defaultDeleteBatch = 200
)

// NewCacheService creates a new cache service instance
func NewCacheService(
	serviceRepo repository.IServiceConfigurationRepository,
	queryObjectRepo repository.IQueryObjectRepository,
	cacheConfig *common_dto.ESACacheConfig,
) *CacheService {
	service := &CacheService{
		cache:           commoninit.GetCache(),
		keyBuilder:      newCacheKeyBuilder(),
		cacheConfig:     cacheConfig,
		logger:          commoninit.GetLogger(),
		serviceRepo:     serviceRepo,
		queryObjectRepo: queryObjectRepo,
		scanCount:       defaultScanCount,
		deleteBatchLen:  defaultDeleteBatch,
	}

	if !commoninit.GetConfigBool("cache.enabled") {
		// Explicitly disabled in config — use the precise sentinel.
		service.mgmtErr = ErrCacheDisabled
		service.logger.Warn("Cache is disabled by configuration; cache management endpoints will return disabled status")
	} else if rc, ok := commoninit.GetCache().(*rediscache.RedisCache); ok {
		service.redisClient = rc.GetUniversalClient()
		service.logger.Info("Cache management using shared Redis client from common-modules")
	} else {
		// cache.enabled=true but Redis was unreachable at startup.
		service.mgmtErr = ErrCacheUnavailable
		service.logger.Warn("Cache unavailable: Redis connection failed at startup; cache management endpoints will return unavailable status")
	}

	service.logResolvedCacheTTLs()
	return service
}

func (cs *CacheService) logResolvedCacheTTLs() {
	if cs.logger == nil {
		return
	}

	defaultTTL, defaultSource := resolveESATTL(cs.cacheConfig, "default")
	serviceConfigTTL, serviceConfigSource := resolveESATTL(cs.cacheConfig, "service_config")
	queryObjectTTL, queryObjectSource := resolveESATTL(cs.cacheConfig, "query_object")

	cs.logger.Infow("ESA cache TTLs resolved",
		"default_ttl", defaultTTL,
		"default_ttl_source", defaultSource,
		"service_config_ttl", serviceConfigTTL,
		"service_config_ttl_source", serviceConfigSource,
		"query_object_ttl", queryObjectTTL,
		"query_object_ttl_source", queryObjectSource)
}

func resolveESATTL(cacheConfig *common_dto.ESACacheConfig, ttlType string) (string, string) {
	if cacheConfig == nil {
		switch ttlType {
		case "service_config":
			return "5m0s", "fallback"
		case "query_object":
			return "10m0s", "fallback"
		default:
			return "1m0s", "fallback"
		}
	}

	switch ttlType {
	case "service_config":
		if cacheConfig.ServiceConfigTTL > 0 {
			return cacheConfig.ServiceConfigTTL.String(), "config"
		}
		return cacheConfig.GetServiceConfigTTL().String(), "fallback"
	case "query_object":
		if cacheConfig.QueryObjectTTL > 0 {
			return cacheConfig.QueryObjectTTL.String(), "config"
		}
		return cacheConfig.GetQueryObjectTTL().String(), "fallback"
	default:
		if cacheConfig.DefaultTTL > 0 {
			return cacheConfig.DefaultTTL.String(), "config"
		}
		return cacheConfig.GetDefaultTTL().String(), "fallback"
	}
}

func (cs *CacheService) fetchServiceConfigurationsFromDB(ctx context.Context, serviceIds []int, reason string, cacheKey string) ([]*esa_models.ServiceConfigurationResponse, error) {
	cs.logger.Infow("Fetching service configurations from database",
		"reason", reason,
		"cache_key", cacheKey,
		"service_ids", serviceIds)
	configs, err := cs.serviceRepo.FindByServiceIdArray(ctx, serviceIds)
	if err != nil {
		cs.logger.Errorw("Failed to fetch service configurations from database",
			"reason", reason,
			"cache_key", cacheKey,
			"service_ids", serviceIds,
			"error", err)
		return nil, err
	}
	cs.logger.Infow("Successfully fetched service configurations from database",
		"reason", reason,
		"cache_key", cacheKey,
		"service_ids", serviceIds,
		"config_count", len(configs))
	return configs, nil
}

func (cs *CacheService) fetchQueryObjectRelationshipsFromDB(ctx context.Context, objectNames []string, reason string, cacheKey string) (map[string]*esa_models.QueryObjectRelationshipMap, error) {
	cs.logger.Infow("Fetching query object relationships from database",
		"reason", reason,
		"cache_key", cacheKey,
		"object_names", objectNames)
	relationships, err := cs.queryObjectRepo.FindByObjectNames(ctx, objectNames)
	if err != nil {
		cs.logger.Errorw("Failed to fetch query object relationships from database",
			"reason", reason,
			"cache_key", cacheKey,
			"object_names", objectNames,
			"error", err)
		return nil, err
	}
	cs.logger.Infow("Successfully fetched query object relationships from database",
		"reason", reason,
		"cache_key", cacheKey,
		"object_names", objectNames,
		"relationship_count", len(relationships))
	return relationships, nil
}

func (cs *CacheService) fetchQueryObjectRelationshipsByLabelsFromDB(ctx context.Context, labels []string, reason string, cacheKey string) (map[string]*esa_models.QueryObjectRelationshipMap, error) {
	cs.logger.Infow("Fetching query object relationships by label from database",
		"reason", reason,
		"cache_key", cacheKey,
		"labels", labels)
	relationships, err := cs.queryObjectRepo.FindByLabels(ctx, labels)
	if err != nil {
		cs.logger.Errorw("Failed to fetch query object relationships by label from database",
			"reason", reason,
			"cache_key", cacheKey,
			"labels", labels,
			"error", err)
		return nil, err
	}
	cs.logger.Infow("Successfully fetched query object relationships by label from database",
		"reason", reason,
		"cache_key", cacheKey,
		"labels", labels,
		"relationship_count", len(relationships))
	return relationships, nil
}

// GetQueryObjectRelationshipsByLabels retrieves query object relationships by label with caching.
// Mirrors GetQueryObjectRelationships but keys on label values and uses FindByLabels.
func (cs *CacheService) GetQueryObjectRelationshipsByLabels(ctx context.Context, labels []string) (map[string]*esa_models.QueryObjectRelationshipMap, error) {
	if cs.cache == nil || cs.keyBuilder == nil {
		cs.logger.Debug("Cache not available, fetching from database")
		return cs.fetchQueryObjectRelationshipsByLabelsFromDB(ctx, labels, "cache_or_key_builder_not_available", "")
	}

	cacheKey := cs.keyBuilder.QueryObjectLabelKey(labels)
	if cacheKey == "" {
		cs.logger.Warn("Failed to generate cache key for query object relationships by label")
		return cs.fetchQueryObjectRelationshipsByLabelsFromDB(ctx, labels, "cache_key_generation_failed", "")
	}

	var cachedData string
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				cs.logger.Warnw("Cache operation panicked, falling back to database", "panic", r, "cache_key", cacheKey)
				err = fmt.Errorf("cache operation failed: %v", r)
			}
		}()
		cachedData, err = cs.cache.Get(ctx, cacheKey)
	}()

	fetchReason := ""
	if err != nil {
		cs.logger.Warnw("Cache get failed, fetching from database", "error", err, "cache_key", cacheKey)
		fetchReason = "cache_get_failed"
	}

	if fetchReason == "" && cachedData != "" {
		var relationships map[string]*esa_models.QueryObjectRelationshipMap
		if err := json.Unmarshal([]byte(cachedData), &relationships); err == nil {
			cs.logger.Debugw("Query object relationships by label retrieved from cache",
				"cache_key", cacheKey,
				"relationship_count", len(relationships))
			return relationships, nil
		}
		cs.logger.Warnw("Failed to unmarshal cached query object relationships by label", "error", err)
		fetchReason = "cache_unmarshal_failed"
	}

	if fetchReason == "" {
		fetchReason = "cache_miss"
	}

	relationships, err := cs.fetchQueryObjectRelationshipsByLabelsFromDB(ctx, labels, fetchReason, cacheKey)
	if err != nil {
		return nil, err
	}

	if len(relationships) > 0 {
		relationshipData, err := json.Marshal(relationships)
		if err == nil {
			ttl := cs.cacheConfig.GetQueryObjectTTL()
			func() {
				defer func() {
					if r := recover(); r != nil {
						cs.logger.Warnw("Cache set operation panicked", "panic", r, "cache_key", cacheKey)
					}
				}()
				if cacheErr := cs.cache.Set(ctx, cacheKey, string(relationshipData), ttl); cacheErr != nil {
					cs.logger.Warnw("Failed to cache query object relationships by label", "error", cacheErr)
				} else {
					cs.logger.Debugw("Query object relationships by label cached successfully",
						"cache_key", cacheKey,
						"relationship_count", len(relationships),
						"ttl", ttl)
				}
			}()
		}
	}

	return relationships, nil
}

// GetServiceConfigurations retrieves service configurations with caching
func (cs *CacheService) GetServiceConfigurations(ctx context.Context, serviceIds []int) ([]*esa_models.ServiceConfigurationResponse, error) {
	if cs.cache == nil || cs.keyBuilder == nil {
		cs.logger.Debug("Cache not available, fetching from database")
		return cs.fetchServiceConfigurationsFromDB(ctx, serviceIds, "cache_or_key_builder_not_available", "")
	}

	// Generate cache key
	cacheKey := cs.keyBuilder.ServiceConfigKey(serviceIds)
	if cacheKey == "" {
		cs.logger.Warn("Failed to generate cache key for service configurations")
		return cs.fetchServiceConfigurationsFromDB(ctx, serviceIds, "cache_key_generation_failed", "")
	}

	// Try to get from cache first with panic recovery
	var cachedData string
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				cs.logger.Warnw("Cache operation panicked, falling back to database", "panic", r, "cache_key", cacheKey)
				err = fmt.Errorf("cache operation failed: %v", r)
			}
		}()
		cachedData, err = cs.cache.Get(ctx, cacheKey)
	}()

	fetchReason := ""
	// If cache operation failed or panicked, fall back to database
	if err != nil {
		cs.logger.Warnw("Cache get failed, fetching from database", "error", err, "cache_key", cacheKey)
		fetchReason = "cache_get_failed"
	}

	if fetchReason == "" && cachedData != "" {
		var configs []*esa_models.ServiceConfigurationResponse
		if err := json.Unmarshal([]byte(cachedData), &configs); err == nil {
			cs.logger.Debugw("Service configurations retrieved from cache",
				"cache_key", cacheKey,
				"config_count", len(configs))
			return configs, nil
		}
		cs.logger.Warnw("Failed to unmarshal cached service configurations", "error", err)
		fetchReason = "cache_unmarshal_failed"
	}

	if fetchReason == "" {
		fetchReason = "cache_miss"
	}

	// Cache miss - fetch from database
	configs, err := cs.fetchServiceConfigurationsFromDB(ctx, serviceIds, fetchReason, cacheKey)
	if err != nil {
		return nil, err
	}

	// Cache the result with panic recovery
	if len(configs) > 0 {
		configData, err := json.Marshal(configs)
		if err == nil {
			// Use ESA-specific TTL from configuration
			ttl := cs.cacheConfig.GetServiceConfigTTL()
			func() {
				defer func() {
					if r := recover(); r != nil {
						cs.logger.Warnw("Cache set operation panicked", "panic", r, "cache_key", cacheKey)
					}
				}()
				if cacheErr := cs.cache.Set(ctx, cacheKey, string(configData), ttl); cacheErr != nil {
					cs.logger.Warnw("Failed to cache service configurations", "error", cacheErr)
				} else {
					cs.logger.Debugw("Service configurations cached successfully",
						"cache_key", cacheKey,
						"config_count", len(configs),
						"ttl", ttl)
				}
			}()
		}
	}

	return configs, nil
}

// GetQueryObjectRelationships retrieves query object relationships with caching
func (cs *CacheService) GetQueryObjectRelationships(ctx context.Context, objectNames []string) (map[string]*esa_models.QueryObjectRelationshipMap, error) {
	if cs.cache == nil || cs.keyBuilder == nil {
		cs.logger.Debug("Cache not available, fetching from database")
		return cs.fetchQueryObjectRelationshipsFromDB(ctx, objectNames, "cache_or_key_builder_not_available", "")
	}

	// Generate cache key
	cacheKey := cs.keyBuilder.QueryObjectKey(objectNames)
	if cacheKey == "" {
		cs.logger.Warn("Failed to generate cache key for query object relationships")
		return cs.fetchQueryObjectRelationshipsFromDB(ctx, objectNames, "cache_key_generation_failed", "")
	}

	// Try to get from cache first with panic recovery
	var cachedData string
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				cs.logger.Warnw("Cache operation panicked, falling back to database", "panic", r, "cache_key", cacheKey)
				err = fmt.Errorf("cache operation failed: %v", r)
			}
		}()
		cachedData, err = cs.cache.Get(ctx, cacheKey)
	}()

	fetchReason := ""
	// If cache operation failed or panicked, fall back to database
	if err != nil {
		cs.logger.Warnw("Cache get failed, fetching from database", "error", err, "cache_key", cacheKey)
		fetchReason = "cache_get_failed"
	}

	if fetchReason == "" && cachedData != "" {
		var relationships map[string]*esa_models.QueryObjectRelationshipMap
		if err := json.Unmarshal([]byte(cachedData), &relationships); err == nil {
			cs.logger.Debugw("Query object relationships retrieved from cache",
				"cache_key", cacheKey,
				"relationship_count", len(relationships))
			return relationships, nil
		}
		cs.logger.Warnw("Failed to unmarshal cached query object relationships", "error", err)
		fetchReason = "cache_unmarshal_failed"
	}

	if fetchReason == "" {
		fetchReason = "cache_miss"
	}

	// Cache miss - fetch from database
	relationships, err := cs.fetchQueryObjectRelationshipsFromDB(ctx, objectNames, fetchReason, cacheKey)
	if err != nil {
		return nil, err
	}

	// Cache the result with panic recovery
	if len(relationships) > 0 {
		relationshipData, err := json.Marshal(relationships)
		if err == nil {
			// Use ESA-specific TTL from configuration
			ttl := cs.cacheConfig.GetQueryObjectTTL()
			func() {
				defer func() {
					if r := recover(); r != nil {
						cs.logger.Warnw("Cache set operation panicked", "panic", r, "cache_key", cacheKey)
					}
				}()
				if cacheErr := cs.cache.Set(ctx, cacheKey, string(relationshipData), ttl); cacheErr != nil {
					cs.logger.Warnw("Failed to cache query object relationships", "error", cacheErr)
				} else {
					cs.logger.Debugw("Query object relationships cached successfully",
						"cache_key", cacheKey,
						"relationship_count", len(relationships),
						"ttl", ttl)
				}
			}()
		}
	}

	return relationships, nil
}

// InvalidateServiceConfigurations removes service configuration cache entries
func (cs *CacheService) InvalidateServiceConfigurations(ctx context.Context, serviceIds []int) error {
	if cs.cache == nil || cs.keyBuilder == nil {
		return nil // No cache to invalidate
	}

	cacheKey := cs.keyBuilder.ServiceConfigKey(serviceIds)
	if cacheKey == "" {
		return fmt.Errorf("failed to generate cache key for service configurations")
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				cs.logger.Warnw("Cache delete operation panicked", "panic", r, "cache_key", cacheKey)
			}
		}()
		if err := cs.cache.Delete(ctx, cacheKey); err != nil {
			cs.logger.Warnw("Failed to invalidate service configuration cache", "error", err, "cache_key", cacheKey)
		} else {
			cs.logger.Debugw("Service configuration cache invalidated successfully", "cache_key", cacheKey)
		}
	}()

	return nil
}

// InvalidateQueryObjectRelationships removes query object relationship cache entries
func (cs *CacheService) InvalidateQueryObjectRelationships(ctx context.Context, objectNames []string) error {
	if cs.cache == nil || cs.keyBuilder == nil {
		return nil // No cache to invalidate
	}

	cacheKey := cs.keyBuilder.QueryObjectKey(objectNames)
	if cacheKey == "" {
		return fmt.Errorf("failed to generate cache key for query object relationships")
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				cs.logger.Warnw("Cache delete operation panicked", "panic", r, "cache_key", cacheKey)
			}
		}()
		if err := cs.cache.Delete(ctx, cacheKey); err != nil {
			cs.logger.Warnw("Failed to invalidate query object relationship cache", "error", err, "cache_key", cacheKey)
		} else {
			cs.logger.Debugw("Query object relationship cache invalidated successfully", "cache_key", cacheKey)
		}
	}()

	return nil
}

// PingCache checks if cache is accessible.
func (cs *CacheService) PingCache(ctx context.Context) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.mgmtErr != nil {
		return cs.mgmtErr
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return cs.redisClient.Ping(ctx).Err()
}

// GetCacheStats returns cache statistics.
func (cs *CacheService) GetCacheStats(ctx context.Context) (map[string]interface{}, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.mgmtErr != nil {
		return nil, cs.mgmtErr
	}
	if ctx == nil {
		ctx = context.Background()
	}

	stats := map[string]interface{}{
		"scan_count":       cs.scanCount,
		"delete_batch_len": cs.deleteBatchLen,
	}

	if info, err := cs.redisClient.Info(ctx).Result(); err == nil {
		stats["redis_info"] = info
	} else {
		stats["redis_info_error"] = err.Error()
	}

	if dbSize, err := cs.redisClient.DBSize(ctx).Result(); err == nil {
		stats["db_size"] = dbSize
	} else {
		stats["db_size_error"] = err.Error()
	}

	return stats, nil
}

// GetValueByKey returns the raw cache value for a given key.
func (cs *CacheService) GetValueByKey(ctx context.Context, key string) (string, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.mgmtErr != nil {
		return "", cs.mgmtErr
	}
	if ctx == nil {
		ctx = context.Background()
	}

	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" {
		return "", fmt.Errorf("cache key is required")
	}

	value, err := cs.redisClient.Get(ctx, trimmedKey).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

// ClearCache clears cache either fully (clear_all=true, uses SCAN+DEL *) or by match pattern.
func (cs *CacheService) ClearCache(ctx context.Context, req ClearCacheRequest) (*ClearCacheResult, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.mgmtErr != nil {
		return nil, cs.mgmtErr
	}
	if ctx == nil {
		ctx = context.Background()
	}

	pattern := "*"
	mode := "all"
	if !req.ClearAll {
		mode = "match"
		pattern = "*" + strings.TrimSpace(req.Match) + "*"
	}

	result := &ClearCacheResult{
		Mode:    mode,
		Pattern: pattern,
		DryRun:  req.DryRun,
		Keys:    make([]string, 0),
	}

	var (
		cursor uint64
		buffer = make([]string, 0, cs.deleteBatchLen)
	)

	for {
		keys, nextCursor, err := cs.redisClient.Scan(ctx, cursor, pattern, cs.scanCount).Result()
		if err != nil {
			return nil, fmt.Errorf("redis scan failed for pattern %q: %w", pattern, err)
		}

		result.ScanCycles++
		result.KeysMatched += len(keys)
		result.Keys = appendLimitedKeys(result.Keys, keys, req.LimitMatchKey)

		for _, key := range keys {
			if req.DryRun {
				continue
			}
			buffer = append(buffer, key)
			if len(buffer) >= cs.deleteBatchLen {
				deleted, delErr := cs.deleteKeysClusterSafe(ctx, buffer)
				if delErr != nil {
					return nil, fmt.Errorf("redis delete failed: %w", delErr)
				}
				result.KeysDeleted += deleted
				buffer = buffer[:0]
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	if !req.DryRun && len(buffer) > 0 {
		deleted, err := cs.deleteKeysClusterSafe(ctx, buffer)
		if err != nil {
			return nil, fmt.Errorf("redis delete failed: %w", err)
		}
		result.KeysDeleted += deleted
	}

	cs.logger.Infow("Cache clear execution completed",
		"mode", result.Mode,
		"pattern", result.Pattern,
		"dryRun", result.DryRun,
		"keysMatched", result.KeysMatched,
		"keysDeleted", result.KeysDeleted,
		"scanCycles", result.ScanCycles)

	return result, nil
}

// Close is a no-op: the underlying Redis client is owned by common-modules and
// closed by commoninit.Shutdown(). It is kept to satisfy ICacheService.
func (cs *CacheService) Close(_ context.Context) error {
	return nil
}

func (cs *CacheService) deleteKeysClusterSafe(ctx context.Context, keys []string) (int64, error) {
	var totalDeleted int64
	for _, key := range keys {
		deleted, err := cs.redisClient.Del(ctx, key).Result()
		if err != nil {
			return totalDeleted, err
		}
		totalDeleted += deleted
	}
	return totalDeleted, nil
}

var _ ICacheService = (*CacheService)(nil)
