package controller

import (
	"errors"
	"esa/internal/app/constants"
	commoninit "esa/internal/app/init"
	"esa/internal/app/service/cache_service"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type CacheController struct {
	CacheService *cache_service.CacheService
}

type ClearCacheAPIRequest struct {
	ClearAll      bool   `json:"clear_all"`
	Match         string `json:"match"`
	DryRun        bool   `json:"dry_run"`
	LimitMatchKey int    `json:"limitMatchKey"`
}

func NewCacheController(cacheService *cache_service.CacheService) *CacheController {
	return &CacheController{
		CacheService: cacheService,
	}
}

func (cc *CacheController) PingCache() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !validateCacheAPIKey(c) {
			return
		}

		if err := cc.CacheService.PingCache(c.Request.Context()); err != nil {
			if handleCacheMgmtErr(c, err) {
				return
			}
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status": "healthy",
		})
	}
}

func (cc *CacheController) GetCacheStats() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !validateCacheAPIKey(c) {
			return
		}

		stats, err := cc.CacheService.GetCacheStats(c.Request.Context())
		if err != nil {
			if handleCacheMgmtErr(c, err) {
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"stats": stats,
		})
	}
}

func (cc *CacheController) GetCacheValueByKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !validateCacheAPIKey(c) {
			return
		}

		key := strings.TrimSpace(c.Query("key"))
		if key == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "query parameter 'key' is required",
			})
			return
		}

		value, err := cc.CacheService.GetValueByKey(c.Request.Context(), key)
		if err != nil {
			if handleCacheMgmtErr(c, err) {
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		if value == "" {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "cache key not found",
				"key":   key,
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"key":   key,
			"value": value,
		})
	}
}

func (cc *CacheController) ClearCache(c *gin.Context) {
	log := commoninit.GetLogger(c.Request.Context())

	if !validateCacheAPIKey(c) {
		return
	}

	if cc.CacheService == nil {
		log.Error("Cache clear service not initialized")
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "cache clear service unavailable",
		})
		return
	}

	var req ClearCacheAPIRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	req.Match = strings.TrimSpace(req.Match)
	if req.ClearAll && req.Match != "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "provide either clear_all=true or match, not both",
		})
		return
	}
	if !req.ClearAll && req.Match == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "either clear_all=true or a non-empty match is required",
		})
		return
	}
	if req.LimitMatchKey < 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "limitMatchKey must be >= 0",
		})
		return
	}

	result, err := cc.CacheService.ClearCache(c.Request.Context(), cache_service.ClearCacheRequest{
		ClearAll:      req.ClearAll,
		Match:         req.Match,
		DryRun:        req.DryRun,
		LimitMatchKey: req.LimitMatchKey,
	})
	if err != nil {
		if handleCacheMgmtErr(c, err) {
			return
		}
		log.Errorw("Failed to clear cache keys", "error", err, "clear_all", req.ClearAll, "match", req.Match, "dry_run", req.DryRun)
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "failed to clear cache keys",
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "cache clear completed",
		"result":  result,
	})
}

// handleCacheMgmtErr checks if err is a known startup-time sentinel (cache
// disabled or unavailable) and writes the appropriate response. Returns true
// if the error was handled so the caller can return immediately.
func handleCacheMgmtErr(c *gin.Context, err error) bool {
	if errors.Is(err, cache_service.ErrCacheDisabled) {
		c.JSON(http.StatusOK, gin.H{"status": "disabled", "message": err.Error()})
		return true
	}
	if errors.Is(err, cache_service.ErrCacheUnavailable) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "message": err.Error()})
		return true
	}
	return false
}

func validateCacheAPIKey(c *gin.Context) bool {
	log := commoninit.GetLogger(c.Request.Context())
	token := c.GetHeader(constants.ApiKey)
	expectedToken := commoninit.GetConfigString(constants.ApiKey, "")
	if expectedToken == "" || token != expectedToken {
		log.Warnw("Invalid API key provided", "provided_key", token)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
		return false
	}
	return true
}
