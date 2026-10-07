package constants

const (

	// DMI base url key
	DMIBaseUrlKey          = "DMIBaseUrl"
	Environment            = "Environment"
	Production             = "production"
	ExternalServiceAdapter = "/external-service-adapter"

	//HTTP methods
	POST  = "POST"
	GET   = "GET"
	PATCH = "PATCH"
	PUT   = "PUT"

	// Swagger
	SwaggerDocJSON = "/swagger/doc.json"

	// Token header
	ContentType                = "Content-Type"
	Accept                     = "accept"
	ApplicationJsonContentType = "application/json"
	Authorization              = "Authorization"
	ApiKey                     = "X-Api-Key"
	CacheAPIKeyConfig          = "cache-api-key"
	Bearer                     = "Bearer "
	CorrelationId              = "X-Correlation-ID"
	RealIpHeaderKey            = "X-REAL-IP"
	ForwardedForHeaderKey      = "X-FORWARDED-FOR"
	RemoteAddressHeaderKey     = "REMOTE-ADDR"
	IpAddress                  = "ip-address"

	// Comma delimiter
	CommaDelimiter = ","

	EmptyString      = ""
	UnderscoreString = "_"

	//Environment keys
	EnvKey                = "BOOT_CUR_ENV"
	DevEnvironment        = "dev"
	Service               = "esa"
	DevConfigJsonFilePath = "configs/config.json"

	// Server
	ServiceName    = "service.name"
	RedisURL       = "redis.url"
	LoggerLevelKey = "log.level"
	Region         = "aws.region"
	ServerPort     = "server.port"
	EnvironmentKey = "Environment"
	// Enum constants
	UnknownEnumValue = "Unknown"

	// HTTP client: max connection retries on connection/transport errors only (not on timeout). 0 = no retry.
	HttpConnectionRetryCountKey = "http.connection_retry_count"

	StringType  = "string"
	BooleanType = "bool"
	FloatType   = "float"
	Json        = "json"
)

const (
	// Authentication configuration
	ENABLE_AUTH   = "EnableAuth"
	JWKS_AUDIENCE = "JwksAudience"
	JWKS_ISSUER   = "JwksIssuer"
	JWKS_URL      = "JwksUrl"
)

const (
	// Database configuration
	DatabaseUserName         = "database.username"
	DatabasePassword         = "database.password"
	DatabaseHost             = "database.host"
	DatabaseName             = "database.name"
	DatabasePort             = "database.port"
	DatabaseDialect          = "database.dialect"
	DbMigrationDir           = "/internal/app/db/migrations"
	MaxIdleConnections       = "database.maxIdleConnections"
	MaxOpenConnections       = "database.maxOpenConnections"
	ConnMaxLifetimeInHours   = "database.connMaxLifetimeInHours"
	ShouldRunAutoMigrations  = "database.shouldRunAutoMigrations"
	ShouldRunGooseMigrations = "database.shouldRunGooseMigrations"
)

// Concurrency limit default values used when no config data is found for the corresponding server.* keys.
// Config overrides these when set (e.g. server.max_concurrent_sequence_requests, server.max_services_per_group).
const (
	DefaultMaxConcurrentSequenceRequests = 100 // used when server.max_concurrent_sequence_requests is unset
	DefaultMaxServicesPerGroup           = 15  // used when server.max_services_per_group is unset
	DefaultFanOutMaxConcurrent           = 5   // used when server.fan_out_max_concurrent is unset
	DefaultMaxSFChunkParallel            = 4   // used when server.max_sf_chunk_parallel is unset
	DefaultConcurrencyBaseReserveMB      = 200 // used when server.concurrency_base_reserve_mb is unset (memory-based limit)
	DefaultMemoryPerRequestMB            = 3   // used when server.concurrency_memory_per_request_mb is unset (memory-based limit)
	DefaultMaxConcurrentMin              = 10  // used when server.max_concurrent_sequence_requests_min is unset (memory-based clamp: min concurrent requests)
	DefaultMaxConcurrentMax              = 500 // used when server.max_concurrent_sequence_requests_max is unset (memory-based clamp: max concurrent requests)
	// Default HTTP connection retries on connection reset/refused (not on timeout). 2 = up to 3 attempts.
	DefaultConnectionRetryCount = 2
)

// Config keys for concurrency limits (server.*)
const (
	ServerMaxConcurrentSequenceRequests    = "server.max_concurrent_sequence_requests"
	ServerMaxServicesPerGroup              = "server.max_services_per_group"
	ServerFanOutMaxConcurrent              = "server.fan_out_max_concurrent"
	ServerMaxSFChunkParallel               = "server.max_sf_chunk_parallel"
	ServerUseMemoryBasedConcurrencyLimit   = "server.use_memory_based_concurrency_limit"
	ServerConcurrencyBaseReserveMB         = "server.concurrency_base_reserve_mb"
	ServerConcurrencyMemoryPerRequestMB    = "server.concurrency_memory_per_request_mb"
	ServerMaxConcurrentSequenceRequestsMin = "server.max_concurrent_sequence_requests_min"
	ServerMaxConcurrentSequenceRequestsMax = "server.max_concurrent_sequence_requests_max"
)

// Audit log: when true, MongoDB audit log insert is synchronous (block response until written, default is false)
const AuditLogSyncKey = "audit.log.sync"

// Async ESA audit log queue/worker config keys and defaults
const (
	AuditLogAsyncESAWorkersKey          = "audit.log.async.esa_workers"
	AuditLogAsyncESAQueueSizeKey        = "audit.log.async.esa_queue_size"
	AuditLogAsyncESATimeoutSecondsKey   = "audit.log.async.esa_timeout_seconds"
	AuditLogAsyncESAInlineFallbackMsKey = "audit.log.async.esa_inline_fallback_ms"

	DefaultAsyncEsaLogWorkers          = 2
	DefaultAsyncEsaLogQueueSize        = 200
	DefaultAsyncEsaLogTimeoutSeconds   = 15
	DefaultAsyncEsaLogInlineFallbackMs = 500 // Mongo InsertMany timeout for queue-full / shutdown inline path
)

// Fan-out source wait tuning defaults (milliseconds)
const (
	DefaultFanOutSourceWaitBufferMs          = 5000
	DefaultFanOutSourceWaitHardCapMs         = 60000
	DefaultFanOutSourcePollIntervalMs        = 20
	DefaultFanOutSourceProgressLogIntervalMs = 5000
)

// Graceful shutdown config keys and defaults
const (
	ServerShutdownHTTPDrainSeconds       = "server.shutdown.http_drain_seconds"
	ServerShutdownReadinessDrainSeconds  = "server.shutdown.readiness_drain_seconds"
	ServerShutdownAsyncLogDrainSeconds   = "server.shutdown.async_log_drain_seconds"
	ServerShutdownAsyncLogWaitSeconds    = "server.shutdown.async_log_wait_seconds"
	ServerShutdownMongoDisconnectSeconds = "server.shutdown.mongo_disconnect_seconds"
	ServerShutdownEmergencySeconds       = "server.shutdown.emergency_seconds"
	ServerShutdownModulesTotalSeconds    = "server.shutdown.modules_total_seconds"
	ServerProcessSequenceMaxSeconds      = "server.process_sequence_max_seconds"

	DefaultShutdownHTTPDrainSeconds       = 30
	DefaultShutdownReadinessDrainSeconds  = 10
	DefaultShutdownAsyncLogDrainSeconds   = 8
	DefaultShutdownAsyncLogWaitSeconds    = 10
	DefaultShutdownMongoDisconnectSeconds = 10
	DefaultShutdownEmergencySeconds       = 10
	DefaultShutdownModulesTotalSeconds    = 45
	DefaultProcessSequenceMaxSeconds      = 120
)

// Runtime telemetry config keys and defaults (low-overhead operational diagnostics).
// Mirrors the decision-manager telemetry contract so both services can be correlated
// during load/capacity testing. Disabled by default; enable via telemetry.enabled=true.
const (
	TelemetryEnabledKey         = "telemetry.enabled"
	TelemetryIntervalSecondsKey = "telemetry.interval_seconds"
	TelemetryMemstatsEnabledKey = "telemetry.memstats.enabled"

	DefaultTelemetryIntervalSeconds = 60
	MinTelemetryIntervalSeconds     = 10
)

// pprof debug server (diagnostics only). Disabled by default; enable via config key
// "pprof.enabled=true" or env ENABLE_PPROF=true. Binds to localhost by default so it is
// reachable only through `kubectl port-forward` / exec, never from other pods or the network.
// Keep disabled in production; turn off once debugging is complete.
const (
	PprofEnabledKey = "pprof.enabled"
	PprofAddrKey    = "pprof.addr"

	DefaultPprofAddr = "127.0.0.1:6060"
)
