package cache_service

import (
	"crypto/md5"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// CacheKeyBuilder provides ESA-specific cache key generation
type CacheKeyBuilder struct {
	prefix string
}

// newCacheKeyBuilder creates a new cache key builder with ESA prefix
// Note: This is now package-private (lowercase) since it's only used within cache_service
func newCacheKeyBuilder() *CacheKeyBuilder {
	return &CacheKeyBuilder{
		prefix: "esa",
	}
}

// ServiceConfigKey generates cache key for service configurations
func (c *CacheKeyBuilder) ServiceConfigKey(serviceIds []int) string {
	if len(serviceIds) == 0 {
		return ""
	}

	// Sort service IDs for consistent key generation
	sortedIds := make([]int, len(serviceIds))
	copy(sortedIds, serviceIds)
	sort.Ints(sortedIds)

	// Convert to string slice
	idStrings := make([]string, len(sortedIds))
	for i, id := range sortedIds {
		idStrings[i] = strconv.Itoa(id)
	}

	// Create key with sorted IDs
	idsStr := strings.Join(idStrings, ",")
	return c.BuildKey("service_configs", idsStr)
}

// QueryObjectKey generates cache key for query object relationships
func (c *CacheKeyBuilder) QueryObjectKey(objectNames []string) string {
	if len(objectNames) == 0 {
		return ""
	}

	// Sort object names for consistent key generation
	sortedNames := make([]string, len(objectNames))
	copy(sortedNames, objectNames)
	sort.Strings(sortedNames)

	// Create key with sorted names
	namesStr := strings.Join(sortedNames, ",")
	return c.BuildKey("query_objects", namesStr)
}

// QueryObjectLabelKey generates cache key for query object relationships looked up by label.
// Keyed separately from QueryObjectKey so object-name and label lookups never collide.
func (c *CacheKeyBuilder) QueryObjectLabelKey(labels []string) string {
	if len(labels) == 0 {
		return ""
	}

	// Sort labels for consistent key generation
	sortedLabels := make([]string, len(labels))
	copy(sortedLabels, labels)
	sort.Strings(sortedLabels)

	labelsStr := strings.Join(sortedLabels, ",")
	return c.BuildKey("query_object_labels", labelsStr)
}

// BuildKey creates a cache key with prefix and parts
func (c *CacheKeyBuilder) BuildKey(prefix string, parts ...string) string {
	if prefix == "" {
		return ""
	}

	keyParts := []string{c.prefix, prefix}
	for _, part := range parts {
		if part != "" {
			keyParts = append(keyParts, part)
		}
	}

	key := strings.Join(keyParts, ":")

	// If key is too long, hash the variable parts
	if len(key) > 250 { // Redis key limit is usually 512MB, but keep it reasonable
		return c.hashLongKey(keyParts)
	}

	return key
}

// ValidateKey validates if a cache key is valid
func (c *CacheKeyBuilder) ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("cache key cannot be empty")
	}

	if len(key) > 250 {
		return fmt.Errorf("cache key too long: %d characters (max 250)", len(key))
	}

	// Check for invalid characters
	if strings.ContainsAny(key, " \t\n\r") {
		return fmt.Errorf("cache key contains invalid whitespace characters")
	}

	return nil
}

// hashLongKey creates a hashed version of long keys
func (c *CacheKeyBuilder) hashLongKey(keyParts []string) string {
	// Keep prefix and first part, hash the rest
	if len(keyParts) < 3 {
		return strings.Join(keyParts, ":")
	}

	prefix := strings.Join(keyParts[:2], ":")
	variableParts := strings.Join(keyParts[2:], ":")

	// Create MD5 hash of variable parts
	hash := md5.Sum([]byte(variableParts))
	hashStr := fmt.Sprintf("%x", hash)

	return fmt.Sprintf("%s:%s", prefix, hashStr[:16]) // Use first 16 chars of hash
}

// ServiceConfigKeyWithHash generates service config key with hash for large service lists
func (c *CacheKeyBuilder) ServiceConfigKeyWithHash(serviceIds []int) string {
	if len(serviceIds) == 0 {
		return ""
	}

	// For large lists, use hash-based key
	if len(serviceIds) > 10 {
		return c.serviceConfigHashKey(serviceIds)
	}

	return c.ServiceConfigKey(serviceIds)
}

// QueryObjectKeyWithHash generates query object key with hash for large object lists
func (c *CacheKeyBuilder) QueryObjectKeyWithHash(objectNames []string) string {
	if len(objectNames) == 0 {
		return ""
	}

	// For large lists, use hash-based key
	if len(objectNames) > 10 {
		return c.queryObjectHashKey(objectNames)
	}

	return c.QueryObjectKey(objectNames)
}

// serviceConfigHashKey creates hash-based key for service configurations
func (c *CacheKeyBuilder) serviceConfigHashKey(serviceIds []int) string {
	// Sort and create hash
	sortedIds := make([]int, len(serviceIds))
	copy(sortedIds, serviceIds)
	sort.Ints(sortedIds)

	idStrings := make([]string, len(sortedIds))
	for i, id := range sortedIds {
		idStrings[i] = strconv.Itoa(id)
	}

	idsStr := strings.Join(idStrings, ",")
	hash := md5.Sum([]byte(idsStr))
	hashStr := fmt.Sprintf("%x", hash)

	return c.BuildKey("service_configs", "hash", hashStr[:16])
}

// queryObjectHashKey creates hash-based key for query objects
func (c *CacheKeyBuilder) queryObjectHashKey(objectNames []string) string {
	// Sort and create hash
	sortedNames := make([]string, len(objectNames))
	copy(sortedNames, objectNames)
	sort.Strings(sortedNames)

	namesStr := strings.Join(sortedNames, ",")
	hash := md5.Sum([]byte(namesStr))
	hashStr := fmt.Sprintf("%x", hash)

	return c.BuildKey("query_objects", "hash", hashStr[:16])
}

// EnvironmentKey generates environment-specific cache keys
func (c *CacheKeyBuilder) EnvironmentKey(env, baseKey string) string {
	if env == "" {
		env = "default"
	}
	return c.BuildKey("env", env, baseKey)
}

// VersionedKey generates versioned cache keys for cache invalidation strategies
func (c *CacheKeyBuilder) VersionedKey(baseKey, version string) string {
	if version == "" {
		version = "v1"
	}
	return c.BuildKey(baseKey, "version", version)
}
