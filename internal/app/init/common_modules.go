package init

import (
	"context"
	"esa/internal/app/constants"
	"esa/internal/app/dto/common_dto"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dmi-infotech/common-modules/go/contracts"
	"github.com/dmi-infotech/common-modules/go/http/client"
	"github.com/dmi-infotech/common-modules/go/infrastructure/cache"
	"github.com/dmi-infotech/common-modules/go/infrastructure/config"
	"github.com/dmi-infotech/common-modules/go/infrastructure/logger"
	"github.com/dmi-infotech/common-modules/go/infrastructure/shutdown"
	"github.com/spf13/viper"
)

// CommonModulesConfig defines which components should be initialized
type CommonModulesConfig struct {
	EnableLogger          bool
	EnableConfig          bool
	EnableCache           bool
	EnableAPIClient       bool
	EnableShutdownManager bool
	EnableUtilityProvider bool
}

// DefaultCommonModulesConfig returns the default configuration for ESA
func DefaultCommonModulesConfig() CommonModulesConfig {
	return CommonModulesConfig{
		EnableLogger:          true,
		EnableConfig:          true,
		EnableCache:           true,
		EnableAPIClient:       true,
		EnableShutdownManager: true,
		EnableUtilityProvider: true,
	}
}

// CommonModules holds all initialized common modules components
type CommonModules struct {
	Logger contracts.Logger
	Config contracts.ConfigProvider
	// ConfigWatcher   *config.K8sConfigWatcher // New: ConfigMap watcher
	ConfigWatcher   *config.VolumeConfigWatcher // New: Volume watcher
	Cache           contracts.CacheProvider
	APIClient       contracts.APIClient
	UtilityProvider contracts.UtilityProvider
	ShutdownManager *shutdown.Manager
	ESACacheConfig  *common_dto.ESACacheConfig
}

// GlobalModules holds the initialized common modules
var GlobalModules *CommonModules
var initMutex sync.RWMutex
var isInitialized bool

// sequenceRequestLimiter caps concurrent process-sequence requests; init via InitSequenceRequestLimiter()
var sequenceRequestLimiter chan struct{}
var sequenceRequestLimiterOnce sync.Once

// trackedGoroutines / trackedGoroutineCount track app-spawned background goroutines so the
// shutdown sequence can wait for them to finish BEFORE closing shared resources like cache.
var trackedGoroutines sync.WaitGroup
var trackedGoroutineCount atomic.Int64

// isShuttingDown guards Shutdown() against re-entrancy so cleanup runs once per lifecycle.
var isShuttingDown bool

// appShutdownSignal is closed when graceful shutdown begins so long-running goroutines
// can exit promptly. Reset on each fresh InitializeCommonModules() call.
var appShutdownSignal = make(chan struct{})
var appShutdownSignalCloseOnce sync.Once

// StartTrackedGoroutine launches fn in a tracked goroutine. WaitForTrackedGoroutines will
// block until all tracked goroutines complete (or the context expires).
func StartTrackedGoroutine(fn func()) {
	if fn == nil {
		return
	}
	trackedGoroutineCount.Add(1)
	trackedGoroutines.Add(1)
	go func() {
		defer trackedGoroutineCount.Add(-1)
		defer trackedGoroutines.Done()
		fn()
	}()
}

// WaitForTrackedGoroutines blocks until all tracked goroutines complete or ctx is done.
// Polling avoids spawning a waiter goroutine that can linger after context timeout.
func WaitForTrackedGoroutines(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if trackedGoroutineCount.Load() == 0 {
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// GetAppShutdownSignal returns a channel that is closed when the app begins graceful shutdown.
func GetAppShutdownSignal() <-chan struct{} {
	initMutex.RLock()
	defer initMutex.RUnlock()
	return appShutdownSignal
}

// isTLSCloseNotifyError reports whether err is the benign TLS close-notify timeout
// emitted when a Redis connection is closed during process shutdown.
// The connection is already closed at this point ("but connection was closed anyway").
func isTLSCloseNotifyError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "tls: failed to send closeNotify alert")
}

// ESAUtilityProvider implements contracts.UtilityProvider for ESA-specific functionality
type ESAUtilityProvider struct{}

// GetForwardedIP returns the forwarded IP from context
func (e *ESAUtilityProvider) GetForwardedIP(ctx context.Context) string {
	// Inline the ContextForwardedIP functionality to avoid import cycle
	if ctx == nil {
		return ""
	}

	// Try to get the forwarded IP from context
	if ip, ok := ctx.Value(constants.ForwardedForHeaderKey).(string); ok {
		return ip
	}

	// Try alternative context key
	if ip, ok := ctx.Value("client_ip").(string); ok {
		return ip
	}

	return ""
}

// NoOpCacheProvider is a cache provider that does nothing when cache is not available
type NoOpCacheProvider struct{}

func (n *NoOpCacheProvider) Get(ctx context.Context, key string) (string, error) {
	return "", nil // NoOp: empty data, no error — caller treats as cache miss
}

func (n *NoOpCacheProvider) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	return nil
}

func (n *NoOpCacheProvider) Delete(ctx context.Context, key string) error {
	return nil
}

func (n *NoOpCacheProvider) Exists(ctx context.Context, key string) (bool, error) {
	return false, nil
}

func (n *NoOpCacheProvider) MGet(ctx context.Context, keys []string) ([]string, error) {
	return nil, nil
}

func (n *NoOpCacheProvider) MSet(ctx context.Context, keyValues map[string]string, ttl time.Duration) error {
	return nil
}

func (n *NoOpCacheProvider) MDelete(ctx context.Context, keys []string) error {
	return nil
}

func (n *NoOpCacheProvider) HGet(ctx context.Context, key, field string) (string, error) {
	return "", nil
}

func (n *NoOpCacheProvider) HSet(ctx context.Context, key, field, value string, ttl time.Duration) error {
	return nil
}

func (n *NoOpCacheProvider) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	return nil, nil
}

func (n *NoOpCacheProvider) HDelete(ctx context.Context, key string, fields ...string) error {
	return nil
}

func (n *NoOpCacheProvider) Ping(ctx context.Context) error {
	return nil
}

func (n *NoOpCacheProvider) FlushAll(ctx context.Context) error {
	return nil
}

func (n *NoOpCacheProvider) Close() error {
	return nil
}

func (n *NoOpCacheProvider) GetStats(ctx context.Context) (contracts.CacheStats, error) {
	return contracts.CacheStats{}, nil
}

// InitializeCommonModulesDefault initializes common modules with default ESA configuration
func InitializeCommonModulesDefault() error {
	return InitializeCommonModules(DefaultCommonModulesConfig())
}

// InitializeCommonModules initializes common modules with the specified configuration
func InitializeCommonModules(config CommonModulesConfig) error {
	initMutex.Lock()
	defer initMutex.Unlock()

	if isInitialized {
		return fmt.Errorf("common modules already initialized")
	}

	// Reset app-level shutdown signal for a fresh lifecycle.
	appShutdownSignal = make(chan struct{})
	appShutdownSignalCloseOnce = sync.Once{}
	isShuttingDown = false

	fmt.Println("🚀 Initializing Common Modules for ESA...")

	// Initialize the global modules container
	GlobalModules = &CommonModules{}

	var err error

	// Step 1: Initialize Logger first for ConfigWatcher logging
	if config.EnableLogger {
		fmt.Println("📝 Initializing Basic Logger...")
		// Initialize with basic configuration first
		GlobalModules.Logger, err = initializeBasicLogger()
		if err != nil {
			return fmt.Errorf("failed to initialize basic logger: %w", err)
		}
		// Test log to confirm basic logger is working and logging to stdout
		GlobalModules.Logger.Info("Basic logger initialized and ready for common-modules logging",
			"output_paths", "stdout",
			"error_output_paths", "stderr")
		fmt.Println("✅ Basic Logger initialized successfully")
	}

	// Step 2: Initialize Configuration Provider with ConfigMap watcher
	if config.EnableConfig {
		fmt.Println("📋 Initializing Configuration Provider with ConfigMap watcher...")
		GlobalModules.Config, GlobalModules.ConfigWatcher, err = initializeConfigWithWatcher(GlobalModules.Logger)
		if err != nil {
			return fmt.Errorf("failed to initialize config: %w", err)
		}

		// Update logger with full configuration now that config is loaded
		if config.EnableLogger {
			fmt.Println("📝 Updating Logger with configuration...")
			GlobalModules.Logger, err = initializeLogger(GlobalModules.Config)
			if err != nil {
				return fmt.Errorf("failed to update logger with config: %w", err)
			}
		}

		fmt.Println("✅ Configuration Provider initialized successfully")
	}

	// Step 3: Initialize Utility Provider
	if config.EnableUtilityProvider {
		fmt.Println("🔧 Initializing Utility Provider...")
		GlobalModules.UtilityProvider = &ESAUtilityProvider{}
		fmt.Println("✅ Utility Provider initialized successfully")
	}

	// Step 4: Initialize ESA Cache Configuration
	if config.EnableCache {
		fmt.Println("⚙️  Initializing ESA Cache Configuration...")
		GlobalModules.ESACacheConfig, err = initializeESACacheConfig(GlobalModules.Config)
		if err != nil {
			if GlobalModules.Logger != nil {
				GlobalModules.Logger.Warnw("Failed to initialize ESA cache configuration", "error", err)
			}
			fmt.Printf("⚠️  ESA cache configuration initialization failed: %v\n", err)
		} else {
			fmt.Println("✅ ESA Cache Configuration initialized successfully")
		}
	}

	// Step 5: Initialize Cache Provider
	if config.EnableCache {
		fmt.Println("🗄️  Initializing Cache Provider...")
		GlobalModules.Cache, err = initializeCache(GlobalModules.Config, GlobalModules.Logger)
		if err != nil {
			// Log warning but don't fail initialization
			if GlobalModules.Logger != nil {
				GlobalModules.Logger.Warnw("Failed to initialize cache, continuing without cache", "error", err)
			}
			fmt.Printf("⚠️  Cache initialization failed, continuing without cache: %v\n", err)
			// CRITICAL FIX: Set cache to nil to prevent shutdown panic
			GlobalModules.Cache = nil
		} else {
			fmt.Println("✅ Cache Provider initialized successfully")
		}
	}

	// Step 6: Initialize API Client
	if config.EnableAPIClient {
		fmt.Println("🌐 Initializing API Client...")
		GlobalModules.APIClient, err = initializeAPIClient(GlobalModules.Config, GlobalModules.Logger, GlobalModules.UtilityProvider)
		if err != nil {
			return fmt.Errorf("failed to initialize API client: %w", err)
		}
		fmt.Println("✅ API Client initialized successfully")
	}

	// Step 7: Initialize Shutdown Manager
	if config.EnableShutdownManager {
		fmt.Println("🛑 Initializing Shutdown Manager...")
		GlobalModules.ShutdownManager, err = initializeShutdownManager(GlobalModules.Config, GlobalModules.Logger)
		if err != nil {
			return fmt.Errorf("failed to initialize shutdown manager: %w", err)
		}
		fmt.Println("✅ Shutdown Manager initialized successfully")
	}

	// Step 8: Start ConfigMap watching if enabled
	if GlobalModules.ConfigWatcher != nil {
		fmt.Println("👁️  Starting ConfigMap watcher...")
		if err := GlobalModules.ConfigWatcher.StartWatching(); err != nil {
			GlobalModules.Logger.Warnw("Failed to start ConfigMap watcher", "error", err)
		} else {
			fmt.Println("✅ ConfigMap watcher started successfully")
		}

		// Register configuration reload callback
		GlobalModules.ConfigWatcher.OnConfigReload(func(newConfig contracts.ConfigProvider) {
			fmt.Println("🔄 Configuration reloaded, updating components...")
			GlobalModules.Config = newConfig

			// Optionally reinitialize components that depend on configuration
			// This is where you can add logic to reload cache connections, API client settings, etc.
			if GlobalModules.Logger != nil {
				GlobalModules.Logger.Infow("Configuration reloaded successfully")
			}
		})
	}

	isInitialized = true
	fmt.Println("🎉 Common Modules initialization completed successfully!")

	// Log initialization summary
	if GlobalModules.Logger != nil {
		GlobalModules.Logger.Infow("Common modules initialized",
			"logger", config.EnableLogger,
			"config", config.EnableConfig,
			"cache", config.EnableCache && GlobalModules.Cache != nil,
			"apiClient", config.EnableAPIClient,
			"shutdownManager", config.EnableShutdownManager,
			"utilityProvider", config.EnableUtilityProvider,
			"esaCacheConfig", config.EnableCache && GlobalModules.ESACacheConfig != nil,
			"configWatcher", GlobalModules.ConfigWatcher != nil)
	}

	return nil
}

// initializeBasicLogger initializes a basic logger without configuration
func initializeBasicLogger() (contracts.Logger, error) {
	logConfig := logger.Config{
		Level:       logger.LogLevel("info"),
		Development: false,
		Encoding:    "json",
		// Note: OutputPaths and ErrorOutputPaths will default to ["stdout"] and ["stderr"] in NewZapLogger
	}

	return logger.NewZapLogger(logConfig)
}

// initializeConfigWithWatcher initializes the configuration provider with ConfigMap watcher
// func initializeConfigWithWatcher(logger contracts.Logger) (contracts.ConfigProvider, *config.K8sConfigWatcher, error) {
// 	// Determine environment
// 	env := os.Getenv("ENVIRONMENT")
// 	if env == "" {
// 		env = os.Getenv("ENV")
// 		if env == "" {
// 			env = "dev" // Default to dev environment
// 		}
// 	}

// 	// Determine namespace for ConfigMap
// 	namespace := getNamespaceForEnvironment(env)

// 	fmt.Printf("Loading configuration for environment: %s, namespace: %s\n", env, namespace)

// 	// Try to initialize ConfigMap watcher
// 	watcherOptions := config.ConfigWatcherOptions{
// 		ServiceName: "external-service-adapter",
// 		Environment: env,
// 		Namespace:   namespace,
// 		Logger:      logger,
// 	}

// 	watcher, err := config.NewK8sConfigWatcher(watcherOptions)
// 	if err != nil {
// 		fmt.Printf("⚠️  Failed to initialize ConfigMap watcher: %v\n", err)
// 		fmt.Println("📁 Falling back to file-based configuration...")

// 		// Fallback to file-based configuration
// 		configProvider, fallbackErr := initializeFallbackConfig(env)
// 		if fallbackErr != nil {
// 			return nil, nil, fmt.Errorf("both ConfigMap and file-based configuration failed: ConfigMap error: %v, Fallback error: %v", err, fallbackErr)
// 		}

// 		return configProvider, nil, nil
// 	}

// 	// Get initial configuration from watcher
// 	configProvider := watcher.GetConfig()
// 	if configProvider == nil {
// 		return nil, nil, fmt.Errorf("failed to get initial configuration from ConfigMap watcher")
// 	}

// 	fmt.Println("✅ ConfigMap-based configuration loaded successfully")
// 	return configProvider, watcher, nil
// }

// decision-manager/internal/app/init/common_modules.go
// Replace the initializeConfigWithWatcher function:

func initializeConfigWithWatcher(logger contracts.Logger) (contracts.ConfigProvider, *config.VolumeConfigWatcher, error) {
	// Log that we're starting config initialization with the passed logger
	logger.Info("=== STARTING initializeConfigWithWatcher ===")
	logger.Info("Starting configuration initialization with volume watcher",
		"function", "initializeConfigWithWatcher")

	// Determine environment
	env := os.Getenv("ENVIRONMENT")
	if env == "" {
		env = os.Getenv("ENV")
		if env == "" {
			env = "dev"
		}
	}

	logger.Info("=== ENVIRONMENT DETERMINED ===", "environment", env)
	fmt.Printf("Loading configuration for environment: %s\n", env)

	// Prepare secret resolver options if AWS secrets are enabled
	var secretResolverOptions *config.SecretResolverOptions

	fmt.Printf("=== LOADING BASIC CONFIG FOR SECRETS ===\n")
	// First check if we can load basic config to read AWS secrets settings
	basicConfig, err := loadBasicConfigForSecrets(env)
	var enableSecrets bool
	var awsRegion, secretPrefix string

	if err == nil && basicConfig != nil {
		fmt.Printf("=== BASIC CONFIG LOADED SUCCESSFULLY ===\n")
		// Read from configuration files first
		enableSecrets = basicConfig.GetBool("aws.secrets.enabled")
		awsRegion = basicConfig.GetString("aws.secrets.region")
		secretPrefix = basicConfig.GetString("aws.secrets.prefix")

		fmt.Printf("AWS secrets config from files: enabled=%t, region=%s, prefix=%s\n", enableSecrets, awsRegion, secretPrefix)
		fmt.Println("🔐 Reading AWS secrets configuration from config files")
	} else {
		fmt.Printf("=== BASIC CONFIG LOADING FAILED ===\n")
		if err != nil {
			fmt.Printf("Error loading basic config: %v\n", err)
		}
		if basicConfig == nil {
			fmt.Printf("Basic config is nil\n")
		}
	}

	// Fall back to environment variables if not found in config
	fmt.Printf("=== CHECKING ENVIRONMENT VARIABLES ===\n")
	if !enableSecrets {
		enableSecretsEnv := os.Getenv("ENABLE_AWS_SECRETS")
		fmt.Printf("ENABLE_AWS_SECRETS env var: %s\n", enableSecretsEnv)
		enableSecrets = enableSecretsEnv == "true"
		fmt.Printf("enableSecrets after env check: %t\n", enableSecrets)
	}
	if awsRegion == "" {
		awsRegion = os.Getenv("AWS_REGION")
		fmt.Printf("AWS_REGION env var: %s\n", awsRegion)
	}
	if secretPrefix == "" {
		secretPrefix = os.Getenv("SECRET_MANAGER_PREFIX")
		fmt.Printf("SECRET_MANAGER_PREFIX env var: %s\n", secretPrefix)
	}

	fmt.Printf("=== FINAL AWS SECRETS CONFIG ===\n")
	fmt.Printf("enableSecrets: %t\n", enableSecrets)
	fmt.Printf("awsRegion: %s\n", awsRegion)
	fmt.Printf("secretPrefix: %s\n", secretPrefix)

	if enableSecrets {
		logger.Info("Initializing AWS secret resolver options",
			"region", awsRegion,
			"prefix", secretPrefix,
			"logger_passed", true)
		secretResolverOptions = &config.SecretResolverOptions{
			Region:       awsRegion,
			SecretPrefix: secretPrefix,
			Logger:       logger,
		}
		fmt.Println("🔐 AWS Secret resolution enabled")
	} else {
		fmt.Println("🔐 AWS Secret resolution DISABLED")
	}

	// Try to initialize Volume ConfigMap watcher
	logger.Info("Creating volume config watcher with options",
		"service_name", "external-service-adapter",
		"environment", env,
		"config_path", "/etc/config",
		"logger_passed", true,
		"secret_resolver_enabled", secretResolverOptions != nil)
	watcherOptions := config.VolumeConfigWatcherOptions{
		ServiceName:           "external-service-adapter",
		Environment:           env,
		ConfigPath:            "/etc/config", // Standard Kubernetes ConfigMap mount path
		Logger:                logger,
		SecretResolverOptions: secretResolverOptions,
	}

	logger.Info("=== ABOUT TO CALL NewVolumeConfigWatcher ===")
	watcher, err := config.NewVolumeConfigWatcher(watcherOptions)
	if err != nil {
		logger.Errorw("=== NewVolumeConfigWatcher FAILED ===", "error", err)
		fmt.Printf("⚠️  Failed to initialize Volume watcher: %v\n", err)
		fmt.Println("📁 Falling back to file-based configuration...")

		// Fallback to file-based configuration
		logger.Info("=== CALLING initializeFallbackConfig ===")
		configProvider, fallbackErr := initializeFallbackConfig(env)
		if fallbackErr != nil {
			logger.Errorw("=== initializeFallbackConfig FAILED ===", "error", fallbackErr)
			return nil, nil, fmt.Errorf("both volume and file-based configuration failed: Volume error: %v, Fallback error: %v", err, fallbackErr)
		}

		logger.Info("=== FALLBACK CONFIG SUCCESSFUL ===")
		return configProvider, nil, nil
	}

	logger.Info("=== NewVolumeConfigWatcher SUCCESSFUL ===")
	// Get initial configuration from watcher
	configProvider := watcher.GetConfig()
	if configProvider == nil {
		return nil, nil, fmt.Errorf("failed to get initial configuration from volume watcher")
	}

	// DEBUG: Log the actual configuration values that were loaded
	logger.Info("=== DEBUGGING LOADED CONFIG ===")
	fmt.Printf("=== DEBUGGING LOADED CONFIG ===\n")

	// Check if config provider is working
	allSettings := configProvider.GetAll()
	logger.Info("All loaded configuration keys", "totalKeys", len(allSettings), "keys", getConfigKeys(allSettings))
	fmt.Printf("Total config keys loaded: %d\n", len(allSettings))

	// Check specific database keys
	dbKeys := []string{"database.host", "database.port", "database.username", "database.password", "database.name"}
	for _, key := range dbKeys {
		value := configProvider.GetString(key)
		if key == "database.password" {
			logger.Info("Config value check", "key", key, "hasValue", value != "", "length", len(value))
			fmt.Printf("Config key %s: hasValue=%t, length=%d\n", key, value != "", len(value))
		} else {
			logger.Info("Config value check", "key", key, "value", value)
			fmt.Printf("Config key %s: %s\n", key, value)
		}
	}

	fmt.Println("✅ Volume-based configuration loaded successfully")
	return configProvider, watcher, nil
}

// Helper function to get configuration keys for debugging
func getConfigKeys(settings map[string]interface{}) []string {
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	return keys
}

// loadBasicConfigForSecrets loads basic configuration to read AWS secrets settings
func loadBasicConfigForSecrets(env string) (contracts.ConfigProvider, error) {
	fmt.Printf("=== ENTERING loadBasicConfigForSecrets ===\n")
	fmt.Printf("Environment: %s\n", env)

	// This function loads config without secret resolution to avoid circular dependency
	v := viper.New()
	v.SetEnvPrefix("ESA")
	v.AutomaticEnv()

	configFound := false

	// Try to load JSON configuration first
	jsonConfigPaths := []string{
		fmt.Sprintf("./configs/config_%s.json", env),
		fmt.Sprintf("./configs/config.json"),
	}

	fmt.Printf("=== TRYING JSON CONFIG PATHS IN loadBasicConfigForSecrets ===\n")
	for _, path := range jsonConfigPaths {
		fmt.Printf("Checking JSON path: %s\n", path)
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("Found JSON file, attempting to load: %s\n", path)
			v.SetConfigFile(path)
			if err := v.ReadInConfig(); err == nil {
				configFound = true
				fmt.Printf("✅ JSON config loaded successfully in loadBasicConfigForSecrets\n")
				break
			} else {
				fmt.Printf("Error reading JSON config: %v\n", err)
			}
		} else {
			fmt.Printf("JSON path does not exist: %s\n", path)
		}
	}

	// Try to merge properties configuration
	propertiesPaths := []string{
		fmt.Sprintf("../configurations/external-service-adapter/external-service-adapter_%s.properties", env),
		fmt.Sprintf("./configs/external-service-adapter_%s.properties", env),
	}

	fmt.Printf("=== TRYING PROPERTIES CONFIG PATHS IN loadBasicConfigForSecrets ===\n")
	for _, path := range propertiesPaths {
		fmt.Printf("Checking properties path: %s\n", path)
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("Found properties file, attempting to load: %s\n", path)
			propViper := viper.New()
			propViper.SetConfigFile(path)
			propViper.SetConfigType("properties")

			if err := propViper.ReadInConfig(); err == nil {
				for key, value := range propViper.AllSettings() {
					v.Set(key, value)
				}
				configFound = true
				fmt.Printf("✅ Properties config loaded successfully in loadBasicConfigForSecrets\n")
			} else {
				fmt.Printf("Error reading properties config: %v\n", err)
			}
		} else {
			fmt.Printf("Properties path does not exist: %s\n", path)
		}
	}

	if !configFound {
		fmt.Printf("=== NO CONFIG FILES FOUND IN loadBasicConfigForSecrets ===\n")
		return nil, fmt.Errorf("no configuration file found")
	}

	// Debug: Check what AWS secrets configuration was loaded
	fmt.Printf("=== BASIC CONFIG LOADED FOR SECRETS ===\n")
	allSettings := v.AllSettings()
	fmt.Printf("Total basic config keys loaded: %d\n", len(allSettings))

	// Check for AWS secrets-related keys specifically
	awsKeys := []string{"aws.secrets.enabled", "aws.secrets.region", "aws.secrets.prefix"}
	for _, key := range awsKeys {
		if value, exists := allSettings[key]; exists {
			fmt.Printf("AWS config key: %s = %v\n", key, value)
		} else {
			fmt.Printf("AWS config key NOT found: %s\n", key)
		}
	}

	return config.NewViperConfigFromExisting(v), nil
}

// initializeFallbackConfig initializes file-based configuration as fallback
func initializeFallbackConfig(env string) (contracts.ConfigProvider, error) {
	fmt.Printf("=== ENTERING initializeFallbackConfig ===\n")
	fmt.Printf("Loading file-based configuration for environment: %s\n", env)

	// Create a new viper instance for merging configurations
	v := viper.New()
	v.SetEnvPrefix("ESA")
	v.AutomaticEnv()

	configFound := false

	// Step 1: Try to load JSON configuration first
	jsonConfigPaths := []string{
		fmt.Sprintf("./configs/config_%s.json", env),
		fmt.Sprintf("./configs/config.json"),
	}

	fmt.Printf("=== TRYING JSON CONFIG PATHS ===\n")
	for _, path := range jsonConfigPaths {
		fmt.Printf("Checking JSON path: %s\n", path)
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("Loading JSON configuration from: %s\n", path)
			v.SetConfigFile(path)
			if err := v.ReadInConfig(); err != nil {
				fmt.Printf("Error reading JSON config %s: %v\n", path, err)
			} else {
				configFound = true
				fmt.Printf("✅ JSON configuration loaded successfully\n")
				break
			}
		} else {
			fmt.Printf("JSON path does not exist: %s\n", path)
		}
	}

	// Step 2: Try to merge properties configuration
	propertiesPaths := []string{
		fmt.Sprintf("../configurations/external-service-adapter/external-service-adapter_%s.properties", env),
		fmt.Sprintf("./configs/external-service-adapter_%s.properties", env),
	}

	fmt.Printf("=== TRYING PROPERTIES CONFIG PATHS ===\n")
	for _, path := range propertiesPaths {
		fmt.Printf("Checking properties path: %s\n", path)
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("Merging properties configuration from: %s\n", path)

			// Create a temporary viper for properties
			propViper := viper.New()
			propViper.SetConfigFile(path)
			propViper.SetConfigType("properties")

			if err := propViper.ReadInConfig(); err != nil {
				fmt.Printf("Error reading properties config %s: %v\n", path, err)
			} else {
				// Merge properties into main config
				for key, value := range propViper.AllSettings() {
					v.Set(key, value)
				}
				configFound = true
				fmt.Printf("✅ Properties configuration merged successfully\n")
			}
		} else {
			fmt.Printf("Properties path does not exist: %s\n", path)
		}
	}

	if !configFound {
		fmt.Printf("=== NO CONFIG FILES FOUND ===\n")
		return nil, fmt.Errorf("no configuration file found in any of the paths")
	}

	// Create configuration provider from the merged viper instance
	configProvider := config.NewViperConfigFromExisting(v)

	// Debug: Check what configuration was loaded
	fmt.Printf("=== FALLBACK CONFIG LOADED ===\n")
	allSettings := v.AllSettings()
	fmt.Printf("Total config keys loaded: %d\n", len(allSettings))

	// Check for database-related keys specifically
	dbKeys := []string{"database.host", "database.port", "database.username", "database.password", "database.name"}
	for _, key := range dbKeys {
		if value, exists := allSettings[key]; exists {
			if key == "database.password" {
				fmt.Printf("Fallback config key: %s = ***\n", key)
			} else {
				fmt.Printf("Fallback config key: %s = %v\n", key, value)
			}
		} else {
			fmt.Printf("Fallback config key NOT found: %s\n", key)
		}
	}

	return configProvider, nil
}

// getNamespaceForEnvironment returns the appropriate namespace for the environment
func getNamespaceForEnvironment(env string) string {
	namespaceMap := map[string]string{
		"dev":         "development",
		"development": "development",
		"staging":     "staging",
		"stage":       "staging",
		"production":  "production",
		"prod":        "production",
		"test":        "testing",
		"testing":     "testing",
	}

	if namespace, exists := namespaceMap[env]; exists {
		return namespace
	}

	// Default to environment name if not mapped
	return env
}

// initializeLogger initializes the logger
func initializeLogger(configProvider contracts.ConfigProvider) (contracts.Logger, error) {
	// Default logger configuration
	logConfig := logger.Config{
		Level:       logger.LogLevel("info"),
		Development: false,
		Encoding:    "json",
		// Note: OutputPaths and ErrorOutputPaths will default to ["stdout"] and ["stderr"] in NewZapLogger
	}

	// Override with configuration if available
	if configProvider != nil {
		if level := configProvider.GetString("log.level"); level != "" {
			logConfig.Level = logger.LogLevel(level)
		}
		if format := configProvider.GetString("log.format"); format != "" {
			logConfig.Encoding = format
		}
		if configProvider.GetBool("log.development") {
			logConfig.Development = true
		}
	}

	return logger.NewZapLogger(logConfig)
}

// initializeESACacheConfig initializes the ESA-specific cache configuration
func initializeESACacheConfig(configProvider contracts.ConfigProvider) (*common_dto.ESACacheConfig, error) {
	if configProvider == nil {
		return nil, fmt.Errorf("configuration provider required for ESA cache configuration initialization")
	}

	// Default ESA cache configuration
	esaCacheConfig := &common_dto.ESACacheConfig{
		Host:             "localhost",
		Port:             6379,
		Password:         "",
		Database:         0,
		PoolSize:         10,
		MinIdleConns:     5,
		MaxRetries:       3,
		DialTimeout:      5 * time.Second,
		ReadTimeout:      3 * time.Second,
		WriteTimeout:     3 * time.Second,
		PoolTimeout:      4 * time.Second,
		DefaultTTL:       1 * time.Minute,
		ServiceConfigTTL: 5 * time.Minute,
		QueryObjectTTL:   10 * time.Minute,
		Enabled:          true,
		Compression:      false,
	}

	// Override with configuration values
	if host := configProvider.GetString("cache.host"); host != "" {
		esaCacheConfig.Host = host
	}
	if port := configProvider.GetInt("cache.port"); port > 0 {
		esaCacheConfig.Port = port
	}
	if password := configProvider.GetString("cache.password"); password != "" {
		esaCacheConfig.Password = password
	}
	if db := configProvider.GetInt("cache.database"); db >= 0 {
		esaCacheConfig.Database = db
	}
	if poolSize := configProvider.GetInt("cache.pool_size"); poolSize > 0 {
		esaCacheConfig.PoolSize = poolSize
	}
	if minIdleConns := configProvider.GetInt("cache.min_idle_conns"); minIdleConns > 0 {
		esaCacheConfig.MinIdleConns = minIdleConns
	}
	if maxRetries := configProvider.GetInt("cache.max_retries"); maxRetries > 0 {
		esaCacheConfig.MaxRetries = maxRetries
	}
	if dialTimeout := configProvider.GetString("cache.dial_timeout"); dialTimeout != "" {
		if duration, err := time.ParseDuration(dialTimeout); err == nil {
			esaCacheConfig.DialTimeout = duration
		}
	}
	if readTimeout := configProvider.GetString("cache.read_timeout"); readTimeout != "" {
		if duration, err := time.ParseDuration(readTimeout); err == nil {
			esaCacheConfig.ReadTimeout = duration
		}
	}
	if writeTimeout := configProvider.GetString("cache.write_timeout"); writeTimeout != "" {
		if duration, err := time.ParseDuration(writeTimeout); err == nil {
			esaCacheConfig.WriteTimeout = duration
		}
	}
	if poolTimeout := configProvider.GetString("cache.pool_timeout"); poolTimeout != "" {
		if duration, err := time.ParseDuration(poolTimeout); err == nil {
			esaCacheConfig.PoolTimeout = duration
		}
	}
	if defaultTTL := configProvider.GetString("cache.default_ttl"); defaultTTL != "" {
		if duration, err := time.ParseDuration(defaultTTL); err == nil {
			esaCacheConfig.DefaultTTL = duration
		}
	}
	if serviceConfigTTL := configProvider.GetString("cache.service_config_ttl"); serviceConfigTTL != "" {
		if duration, err := time.ParseDuration(serviceConfigTTL); err == nil {
			esaCacheConfig.ServiceConfigTTL = duration
		}
	}
	if queryObjectTTL := configProvider.GetString("cache.query_object_ttl"); queryObjectTTL != "" {
		if duration, err := time.ParseDuration(queryObjectTTL); err == nil {
			esaCacheConfig.QueryObjectTTL = duration
		}
	}
	if enabled := configProvider.GetBool("cache.enabled"); !enabled {
		esaCacheConfig.Enabled = false
	}
	if compression := configProvider.GetBool("cache.compression"); compression {
		esaCacheConfig.Compression = true
	}

	return esaCacheConfig, nil
}

// initializeCache initializes the cache provider using the new cache configuration structure
func initializeCache(configProvider contracts.ConfigProvider, logger contracts.Logger) (contracts.CacheProvider, error) {
	if configProvider == nil {
		return nil, fmt.Errorf("configuration provider required for cache initialization")
	}

	// Check if cache is disabled
	if enabled := configProvider.GetBool("cache.enabled"); !enabled {
		if logger != nil {
			logger.Infow("Cache is disabled by configuration", "cache.enabled", false)
		}
		return nil, fmt.Errorf("cache is disabled by configuration")
	}

	// Default cache configuration for common-modules compatibility
	cacheConfig := &contracts.CacheConfig{
		Host:     "localhost",
		Port:     6379,
		Password: "",
		Database: 0,
		Enabled:  true,
	}

	// Override with configuration - use the same keys as ESA cache config
	if host := configProvider.GetString("cache.host"); host != "" {
		cacheConfig.Host = host
	}
	if port := configProvider.GetInt("cache.port"); port > 0 {
		cacheConfig.Port = port
	}
	if password := configProvider.GetString("cache.password"); password != "" {
		cacheConfig.Password = password
	}
	if db := configProvider.GetInt("cache.database"); db >= 0 {
		cacheConfig.Database = db
	}
	if configProvider.IsSet("cache.tls_enabled") {
		cacheConfig.TLSEnabled = configProvider.GetBool("cache.tls_enabled")
	} else {
		cacheConfig.TLSEnabled = true // default true for MemoryDB
	}
	if configProvider.IsSet("cache.tls_insecure_skip_verify") {
		cacheConfig.TLSInsecureSkipVerify = configProvider.GetBool("cache.tls_insecure_skip_verify")
	} else {
		cacheConfig.TLSInsecureSkipVerify = true // default true for MemoryDB (match redis-cli --insecure)
	}
	if username := configProvider.GetString("cache.username"); username != "" {
		cacheConfig.Username = username
	}

	// Log the configuration being used (without sensitive data)
	if logger != nil {
		logger.Infow("Initializing Redis cache with configuration",
			"host", cacheConfig.Host,
			"port", cacheConfig.Port,
			"database", cacheConfig.Database,
			"hasPassword", cacheConfig.Password != "",
			"tlsEnabled", cacheConfig.TLSEnabled,
			"hasUsername", cacheConfig.Username != "",
			"enabled", cacheConfig.Enabled)
	}

	// Attempt to create Redis cache with enhanced error handling
	redisCache, err := cache.NewRedisCache(cacheConfig)
	if err != nil {
		// Provide more detailed error information
		if logger != nil {
			logger.Errorw("Failed to initialize Redis cache",
				"error", err,
				"host", cacheConfig.Host,
				"port", cacheConfig.Port,
				"database", cacheConfig.Database)
		}
		return nil, fmt.Errorf("failed to initialize Redis cache at %s:%d: %w", cacheConfig.Host, cacheConfig.Port, err)
	}

	if logger != nil {
		logger.Infow("Redis cache initialized successfully",
			"host", cacheConfig.Host,
			"port", cacheConfig.Port,
			"database", cacheConfig.Database)
	}

	return redisCache, nil
}

// initializeAPIClient initializes the API client
func initializeAPIClient(configProvider contracts.ConfigProvider, logger contracts.Logger, utilityProvider contracts.UtilityProvider) (contracts.APIClient, error) {
	// Default API client configuration; ESA uses connection pooling (staged transition)
	clientConfig := client.ClientConfig{
		DefaultTimeout:         30,
		DefaultRetryCount:      3,
		DefaultHeaders:         make(map[string]string),
		UseConnectionPool:      true,
		MaxIdleConns:           100,
		MaxIdleConnsPerHost:    10,
		MaxConnsPerHost:        25,
		IdleConnTimeoutSeconds: 90,
	}

	// Override with configuration if available
	if configProvider != nil {
		if timeout := configProvider.GetInt("RestExecuteTimeoutInSeconds"); timeout > 0 {
			clientConfig.DefaultTimeout = timeout
		}
		if retryCount := configProvider.GetInt("http.retry.count"); retryCount > 0 {
			clientConfig.DefaultRetryCount = retryCount
		}
		if v := configProvider.GetInt("http.max_idle_conns"); v > 0 {
			clientConfig.MaxIdleConns = v
		}
		if v := configProvider.GetInt("http.max_idle_conns_per_host"); v > 0 {
			clientConfig.MaxIdleConnsPerHost = v
		}
		if v := configProvider.GetInt("http.max_conns_per_host"); v > 0 {
			clientConfig.MaxConnsPerHost = v
		}
		if v := configProvider.GetInt("http.idle_conn_timeout_seconds"); v > 0 {
			clientConfig.IdleConnTimeoutSeconds = v
		}
	}

	// Set default headers
	clientConfig.DefaultHeaders["User-Agent"] = "ESA/1.0"

	return client.NewAPIClient(clientConfig, logger, utilityProvider), nil
}

// initializeShutdownManager initializes the shutdown manager
func initializeShutdownManager(configProvider contracts.ConfigProvider, logger contracts.Logger) (*shutdown.Manager, error) {
	// Default shutdown configuration
	shutdownConfig := shutdown.Config{
		ShutdownTimeout: 30,
		Logger:          logger,
	}

	// Override with configuration if available
	if configProvider != nil {
		if timeout := configProvider.GetInt("server.shutdown.timeout"); timeout > 0 {
			shutdownConfig.ShutdownTimeout = time.Duration(timeout) * time.Second
		}
	}

	return shutdown.NewManager(shutdownConfig), nil
}

// Shutdown gracefully shuts down all initialized components.
// Order matters:
//  1. Broadcast shutdown signal so long-running goroutines can exit.
//  2. Stop config watcher (avoids late callbacks).
//  3. Run shutdown hooks (sequence service, mongo, etc.).
//  4. Drain tracked in-flight goroutines BEFORE closing the cache client. Closing the
//     business cache first would surface as `redis: client is closed` for any goroutine
//     that is still mid-Set/Get.
//  5. Close the business cache client (treating TLS close-notify timeout as non-fatal).
//     The cache management endpoints share this same client via GetUniversalClient(), so
//     no separate close is needed — the HTTP server is already stopped at this point.
func Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	initMutex.Lock()
	if isShuttingDown {
		initMutex.Unlock()
		return nil
	}
	if !isInitialized || GlobalModules == nil {
		initMutex.Unlock()
		return nil
	}
	isShuttingDown = true
	modules := GlobalModules
	logger := modules.Logger
	shutdownManager := modules.ShutdownManager
	cacheProvider := modules.Cache
	configWatcher := modules.ConfigWatcher
	initMutex.Unlock()

	var errors []string
	shutdownSucceeded := false
	defer func() {
		initMutex.Lock()
		defer initMutex.Unlock()
		if shutdownSucceeded {
			isInitialized = false
			if GlobalModules == modules {
				GlobalModules = nil
			}
		}
		isShuttingDown = false
	}()

	// Step 1: Broadcast shutdown to long-running goroutines before waiting.
	appShutdownSignalCloseOnce.Do(func() {
		close(appShutdownSignal)
	})

	// Step 2: Stop config watcher early so it doesn't continue emitting callbacks during shutdown.
	if configWatcher != nil {
		configWatcher.Stop()
	}

	// Step 3: Run registered shutdown hooks (sequence service close, mongo disconnect,
	// cache management redis client close, ...).
	if shutdownManager != nil {
		if err := shutdownManager.ShutdownWithContext(ctx); err != nil {
			errors = append(errors, fmt.Sprintf("shutdown manager: %v", err))
		}
	}

	// Step 4: Drain in-flight tracked goroutines BEFORE closing the cache client.
	// If cache is closed first, goroutines still running will get "redis: client is closed".
	if err := WaitForTrackedGoroutines(ctx); err != nil {
		errors = append(errors, fmt.Sprintf("tracked goroutines: %v", err))
	}

	// Step 5: Close the business cache client with panic recovery and benign TLS handling.
	if cacheProvider != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					errors = append(errors, fmt.Sprintf("cache close panic: %v", r))
					if logger != nil {
						logger.Errorw("Cache close operation panicked during shutdown", "panic", r)
					}
				}
			}()
			if err := cacheProvider.Close(); err != nil {
				if isTLSCloseNotifyError(err) {
					if logger != nil {
						logger.Infow("Cache Redis connection closed (TLS close-notify timed out, connection was already closed)", "detail", err.Error())
					}
				} else {
					errors = append(errors, fmt.Sprintf("cache: %v", err))
				}
			}
		}()
	}

	if len(errors) > 0 {
		return fmt.Errorf("shutdown errors: %s", strings.Join(errors, ", "))
	}

	shutdownSucceeded = true
	fmt.Println("🛑 Common Modules shutdown completed")

	return nil
}

// Helper functions to get initialized components
func GetLogger(ctx ...context.Context) contracts.Logger {
	initMutex.RLock()
	defer initMutex.RUnlock()

	if GlobalModules == nil || GlobalModules.Logger == nil {
		panic("Logger not initialized. Call InitializeCommonModules() with EnableLogger=true first")
	}

	// If context is provided, return logger with context
	if len(ctx) > 0 && ctx[0] != nil {
		return GlobalModules.Logger.WithContext(ctx[0])
	}

	return GlobalModules.Logger
}

// GetLoggerSafe returns the global logger when initialized, otherwise nil (never panics).
// Use this from components that may run outside a fully initialized process (e.g. unit tests
// constructing helpers directly); callers must nil-check before logging.
func GetLoggerSafe(ctx ...context.Context) contracts.Logger {
	initMutex.RLock()
	defer initMutex.RUnlock()

	if GlobalModules == nil || GlobalModules.Logger == nil {
		return nil
	}

	if len(ctx) > 0 && ctx[0] != nil {
		return GlobalModules.Logger.WithContext(ctx[0])
	}
	return GlobalModules.Logger
}

func GetConfig() contracts.ConfigProvider {
	initMutex.RLock()
	defer initMutex.RUnlock()

	if GlobalModules == nil || GlobalModules.Config == nil {
		panic("Config not initialized. Call InitializeCommonModules() with EnableConfig=true first")
	}
	return GlobalModules.Config
}

func GetCache() contracts.CacheProvider {
	initMutex.RLock()
	defer initMutex.RUnlock()

	if GlobalModules == nil || GlobalModules.Cache == nil {
		// Return a no-op cache implementation instead of panicking
		return &NoOpCacheProvider{}
	}
	return GlobalModules.Cache
}

func GetAPIClient() contracts.APIClient {
	initMutex.RLock()
	defer initMutex.RUnlock()

	if GlobalModules == nil || GlobalModules.APIClient == nil {
		panic("APIClient not initialized. Call InitializeCommonModules() with EnableAPIClient=true first")
	}
	return GlobalModules.APIClient
}

func GetUtilityProvider() contracts.UtilityProvider {
	initMutex.RLock()
	defer initMutex.RUnlock()

	if GlobalModules == nil || GlobalModules.UtilityProvider == nil {
		panic("UtilityProvider not initialized. Call InitializeCommonModules() with EnableUtilityProvider=true first")
	}
	return GlobalModules.UtilityProvider
}

func GetShutdownManager() *shutdown.Manager {
	initMutex.RLock()
	defer initMutex.RUnlock()

	if GlobalModules == nil || GlobalModules.ShutdownManager == nil {
		panic("ShutdownManager not initialized. Call InitializeCommonModules() with EnableShutdownManager=true first")
	}
	return GlobalModules.ShutdownManager
}

func GetESACacheConfig() *common_dto.ESACacheConfig {
	initMutex.RLock()
	defer initMutex.RUnlock()

	if GlobalModules == nil || GlobalModules.ESACacheConfig == nil {
		panic("ESACacheConfig not initialized. Call InitializeCommonModules() with EnableCache=true first")
	}
	return GlobalModules.ESACacheConfig
}

// InitSequenceRequestLimiter initializes the process-sequence concurrency limiter from config (and optional memory-based limit).
// Call once after InitializeCommonModules from main.
func InitSequenceRequestLimiter() {
	sequenceRequestLimiterOnce.Do(func() {
		cap := computeMaxConcurrentSequenceRequests()
		sequenceRequestLimiter = make(chan struct{}, cap)
		if GlobalModules != nil && GlobalModules.Logger != nil {
			GlobalModules.Logger.Infow("Sequence request limiter initialized", "max_concurrent_sequence_requests", cap)
		}
	})
}

func computeMaxConcurrentSequenceRequests() int {
	useMemoryBased := GetConfigBool(constants.ServerUseMemoryBasedConcurrencyLimit, false)
	if useMemoryBased {
		memLimitStr := os.Getenv("MEMORY_LIMIT_BYTES")
		if memLimitStr != "" {
			memLimitBytes, err := strconv.ParseInt(memLimitStr, 10, 64)
			if err == nil && memLimitBytes > 0 {
				baseReserveMB := GetConfigInt(constants.ServerConcurrencyBaseReserveMB, constants.DefaultConcurrencyBaseReserveMB)
				memPerReqMB := GetConfigInt(constants.ServerConcurrencyMemoryPerRequestMB, constants.DefaultMemoryPerRequestMB)
				minCap := GetConfigInt(constants.ServerMaxConcurrentSequenceRequestsMin, constants.DefaultMaxConcurrentMin)
				maxCap := GetConfigInt(constants.ServerMaxConcurrentSequenceRequestsMax, constants.DefaultMaxConcurrentMax)
				availableMB := (int(memLimitBytes) / (1024 * 1024)) - baseReserveMB
				if availableMB <= 0 {
					availableMB = 1
				}
				n := availableMB / memPerReqMB
				if n < minCap {
					n = minCap
				}
				if n > maxCap {
					n = maxCap
				}
				return n
			}
		}
	}
	return GetConfigInt(constants.ServerMaxConcurrentSequenceRequests, constants.DefaultMaxConcurrentSequenceRequests)
}

// TryAcquireSequenceRequest acquires a slot for a process-sequence request. Returns true if acquired, false if at capacity.
func TryAcquireSequenceRequest() bool {
	if sequenceRequestLimiter == nil {
		return true
	}
	select {
	case sequenceRequestLimiter <- struct{}{}:
		return true
	default:
		return false
	}
}

// ReleaseSequenceRequest releases a slot; must only be called after a successful TryAcquireSequenceRequest.
func ReleaseSequenceRequest() {
	if sequenceRequestLimiter != nil {
		<-sequenceRequestLimiter
	}
}

// SequenceLimiterStats reports the current in-flight process-sequence count and the configured
// capacity. Returns (0, 0) when the limiter is not initialized (no cap applied).
// Used by runtime telemetry to show how close a pod is to its concurrency ceiling, which is the
// key input for per-pod capacity sizing.
func SequenceLimiterStats() (inFlight int, capacity int) {
	if sequenceRequestLimiter == nil {
		return 0, 0
	}
	return len(sequenceRequestLimiter), cap(sequenceRequestLimiter)
}

// Helper functions to check if components are available
func IsLoggerAvailable() bool {
	initMutex.RLock()
	defer initMutex.RUnlock()
	return GlobalModules != nil && GlobalModules.Logger != nil
}

func IsConfigAvailable() bool {
	initMutex.RLock()
	defer initMutex.RUnlock()
	return GlobalModules != nil && GlobalModules.Config != nil
}

func IsCacheAvailable() bool {
	initMutex.RLock()
	defer initMutex.RUnlock()
	return GlobalModules != nil && GlobalModules.Cache != nil
}

// GetCacheStatus returns detailed cache status information for debugging
func GetCacheStatus() map[string]interface{} {
	initMutex.RLock()
	defer initMutex.RUnlock()

	status := map[string]interface{}{
		"initialized": false,
		"available":   false,
		"error":       nil,
	}

	if GlobalModules == nil {
		status["error"] = "GlobalModules is nil"
		return status
	}

	status["initialized"] = true

	if GlobalModules.Cache != nil {
		status["available"] = true
	} else {
		status["error"] = "Cache is nil (likely failed to initialize)"
	}

	return status
}

func IsAPIClientAvailable() bool {
	initMutex.RLock()
	defer initMutex.RUnlock()
	return GlobalModules != nil && GlobalModules.APIClient != nil
}

func IsUtilityProviderAvailable() bool {
	initMutex.RLock()
	defer initMutex.RUnlock()
	return GlobalModules != nil && GlobalModules.UtilityProvider != nil
}

func IsShutdownManagerAvailable() bool {
	initMutex.RLock()
	defer initMutex.RUnlock()
	return GlobalModules != nil && GlobalModules.ShutdownManager != nil
}

func IsESACacheConfigAvailable() bool {
	initMutex.RLock()
	defer initMutex.RUnlock()
	return GlobalModules != nil && GlobalModules.ESACacheConfig != nil
}

// IsInitialized returns whether common modules have been initialized
func IsInitialized() bool {
	initMutex.RLock()
	defer initMutex.RUnlock()
	return isInitialized
}

// GetInitializationStatus returns detailed status of all components
func GetInitializationStatus() map[string]bool {
	initMutex.RLock()
	defer initMutex.RUnlock()

	status := make(map[string]bool)
	status["initialized"] = isInitialized
	status["logger"] = IsLoggerAvailable()
	status["config"] = IsConfigAvailable()
	status["cache"] = IsCacheAvailable()
	status["apiClient"] = IsAPIClientAvailable()
	status["utilityProvider"] = IsUtilityProviderAvailable()
	status["shutdownManager"] = IsShutdownManagerAvailable()
	status["esaCacheConfig"] = IsESACacheConfigAvailable()

	return status
}

// GetConfigValue provides a centralized way to get configuration values with fallbacks
func GetConfigValue(key string, defaultValue interface{}) interface{} {
	initMutex.RLock()
	defer initMutex.RUnlock()

	if GlobalModules == nil || GlobalModules.Config == nil {
		return defaultValue
	}

	config := GlobalModules.Config

	// Check if the exact key exists first
	if config.IsSet(key) {
		switch defaultValue.(type) {
		case string:
			return config.GetString(key)
		case int:
			return config.GetInt(key)
		case bool:
			return config.GetBool(key)
		case float64:
			return config.GetFloat64(key)
		case []string:
			return config.GetStringSlice(key)
		default:
			return config.GetString(key)
		}
	}

	return defaultValue
}

// GetConfigString returns a string configuration value with fallback
func GetConfigString(key string, defaultValue ...string) string {
	defaultVal := ""
	if len(defaultValue) > 0 {
		defaultVal = defaultValue[0]
	}
	return GetConfigValue(key, defaultVal).(string)
}

// GetConfigInt returns an int configuration value with fallback
func GetConfigInt(key string, defaultValue ...int) int {
	defaultVal := 0
	if len(defaultValue) > 0 {
		defaultVal = defaultValue[0]
	}
	return GetConfigValue(key, defaultVal).(int)
}

// GetConfigBool returns a bool configuration value with fallback
func GetConfigBool(key string, defaultValue ...bool) bool {
	defaultVal := false
	if len(defaultValue) > 0 {
		defaultVal = defaultValue[0]
	}
	return GetConfigValue(key, defaultVal).(bool)
}

// GetConfigStringSlice returns a string slice configuration value with fallback
func GetConfigStringSlice(key string, defaultValue ...[]string) []string {
	var defaultVal []string
	if len(defaultValue) > 0 {
		defaultVal = defaultValue[0]
	}
	return GetConfigValue(key, defaultVal).([]string)
}
