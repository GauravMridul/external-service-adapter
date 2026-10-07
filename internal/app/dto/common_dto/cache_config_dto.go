package common_dto

import "time"

// ESACacheConfig represents ESA-specific cache configuration
type ESACacheConfig struct {
	// Connection settings (inherited from common-modules generic config)
	Host     string `mapstructure:"host" json:"host"`
	Port     int    `mapstructure:"port" json:"port"`
	Password string `mapstructure:"password" json:"password"`
	Database int    `mapstructure:"database" json:"database"`

	// Pool settings
	PoolSize     int `mapstructure:"pool_size" json:"pool_size"`
	MinIdleConns int `mapstructure:"min_idle_conns" json:"min_idle_conns"`
	MaxRetries   int `mapstructure:"max_retries" json:"max_retries"`

	// Timeout settings
	DialTimeout  time.Duration `mapstructure:"dial_timeout" json:"dial_timeout"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout" json:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout" json:"write_timeout"`
	PoolTimeout  time.Duration `mapstructure:"pool_timeout" json:"pool_timeout"`

	// Generic TTL
	DefaultTTL time.Duration `mapstructure:"default_ttl" json:"default_ttl"`

	// ESA-specific TTL settings
	ServiceConfigTTL time.Duration `mapstructure:"service_config_ttl" json:"service_config_ttl"`
	QueryObjectTTL   time.Duration `mapstructure:"query_object_ttl" json:"query_object_ttl"`

	// Feature flags
	Enabled     bool `mapstructure:"enabled" json:"enabled"`
	Compression bool `mapstructure:"compression" json:"compression"`
}

// GetServiceConfigTTL returns the TTL for service configuration cache
func (c *ESACacheConfig) GetServiceConfigTTL() time.Duration {
	if c.ServiceConfigTTL > 0 {
		return c.ServiceConfigTTL
	}
	// Default to 5 minutes if not configured
	return 5 * time.Minute
}

// GetQueryObjectTTL returns the TTL for query object cache
func (c *ESACacheConfig) GetQueryObjectTTL() time.Duration {
	if c.QueryObjectTTL > 0 {
		return c.QueryObjectTTL
	}
	// Default to 10 minutes if not configured
	return 10 * time.Minute
}

// GetDefaultTTL returns the default TTL for generic cache operations
func (c *ESACacheConfig) GetDefaultTTL() time.Duration {
	if c.DefaultTTL > 0 {
		return c.DefaultTTL
	}
	// Default to 1 minute if not configured
	return 1 * time.Minute
}
