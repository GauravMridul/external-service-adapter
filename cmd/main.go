package main

//This branch is for logging secrets resolution in case issue of secrets or config resolution

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	_ "esa/docs"
	"esa/internal/app/api/debugserver"
	"esa/internal/app/api/router"
	"esa/internal/app/constants"
	"esa/internal/app/db"
	"esa/internal/app/db/repository"
	commoninit "esa/internal/app/init"
	"esa/internal/app/shutdown"
	"esa/middleware/auth"

	"github.com/gin-gonic/gin"
)

func main() {
	// Comprehensive panic recovery for the entire application lifecycle
	defer func() {
		if r := recover(); r != nil {
			// Get stack trace for debugging
			stackTrace := debug.Stack()

			// Try to use the common logger if available, otherwise fallback to standard log
			if logger := commoninit.GetLogger(); logger != nil {
				logger.Errorw("🚨 Critical application panic - initiating emergency shutdown",
					"panic", r,
					"stackTrace", string(stackTrace))
			} else {
				log.Printf("🚨 Critical application panic during early initialization: %v\nStack trace:\n%s", r, stackTrace)
			}

			// Attempt graceful shutdown of any initialized components
			performEmergencyShutdown()

			// Exit with error code
			os.Exit(1)
		}
	}()

	// Initialize and run the application
	if err := runApplication(); err != nil {
		log.Printf("❌ Application failed to start: %v", err)
		performEmergencyShutdown()
		os.Exit(1)
	}
}

// runApplication contains the main application logic with proper error handling
func runApplication() error {
	// Initialize Common Modules with all components
	fmt.Println("🚀 Initializing Common Modules for ESA...")

	// Initialize using the default configuration
	if err := commoninit.InitializeCommonModules(commoninit.DefaultCommonModulesConfig()); err != nil {
		return fmt.Errorf("failed to initialize common modules: %v", err)
	}

	logger := commoninit.GetLogger()
	logger.Info("🚀 ESA Microservice starting up...")

	// Log initialization status
	status := commoninit.GetInitializationStatus()
	logger.Infow("Common modules initialized successfully",
		"config_available", status["config"],
		"cache_available", status["cache"],
		"api_client_available", status["apiClient"])

	commoninit.InitSequenceRequestLimiter()

	// Start the pprof debug server if enabled (no-op when disabled). Diagnostics only.
	debugserver.Start()

	// Initialize all components with comprehensive error handling
	if err := initializeAllComponents(logger); err != nil {
		return fmt.Errorf("component initialization failed: %v", err)
	}

	// Shutdown hooks (async ESA log queue close, Mongo disconnect) are registered when the router is created in startHTTPServer

	// Start the HTTP server with error handling
	httpServer, err := startHTTPServer(logger)
	if err != nil {
		return fmt.Errorf("failed to start HTTP server: %v", err)
	}

	// Set up graceful shutdown handling
	return handleGracefulShutdown(logger, httpServer)
}

// initializeAllComponents initializes all application components with error recovery
func initializeAllComponents(logger interface{}) error {
	log := logger.(interface {
		Info(...interface{})
		Infow(string, ...interface{})
		Errorw(string, ...interface{})
	})

	// Database initialization with error recovery
	log.Info("🗄️ Initializing databases...")
	if err := safeExecute("Database initialization", func() error {
		return initializeDatabases(logger)
	}, log); err != nil {
		return err
	}
	log.Info("✅ All databases initialized successfully")

	// Authentication middleware initialization
	log.Info("🔐 Initializing authentication middleware...")
	if err := safeExecute("Authentication middleware initialization", func() error {
		auth.Init()
		return nil
	}, log); err != nil {
		log.Errorw("Failed to initialize authentication middleware", "error", err)
		return err
	}
	log.Info("✅ Authentication middleware initialized successfully")

	return nil
}

// startHTTPServer initializes and starts the HTTP server
func startHTTPServer(logger interface{}) (*http.Server, error) {
	log := logger.(interface {
		Info(...interface{})
		Infow(string, ...interface{})
		Errorw(string, ...interface{})
	})

	// Initialize router with error recovery
	var ginRouter *gin.Engine
	if err := safeExecute("Router initialization", func() error {
		ginRouter = router.NewRouter()
		return nil
	}, log); err != nil {
		return nil, err
	}

	// Get server configuration
	port := commoninit.GetConfigString("server.port", ":8080")

	// Ensure port has colon prefix for proper address format
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}

	// Create HTTP server
	httpServer := &http.Server{
		Addr:    port,
		Handler: ginRouter,
	}

	// Start server in goroutine with panic recovery
	serverStarted := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Errorw("HTTP server panicked during startup", "panic", r, "stackTrace", string(debug.Stack()))
				serverStarted <- fmt.Errorf("server startup panic: %v", r)
			}
		}()

		log.Infow("Starting HTTP server", "port", port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Errorw("HTTP server failed", "error", err)
			serverStarted <- err
		} else {
			serverStarted <- nil
		}
	}()

	// Wait a moment to see if server starts successfully
	select {
	case err := <-serverStarted:
		if err != nil {
			return nil, err
		}
	case <-time.After(2 * time.Second):
		// Server seems to have started successfully
	}

	log.Info("✅ Server started successfully")
	return httpServer, nil
}

// handleGracefulShutdown sets up signal handling and performs graceful shutdown.
// First step: mark shutting down so readiness fails and traffic moves to other pods.
// Then drain in-flight HTTP requests, then shut down common modules (async log, Mongo, cache).
// Pod restarts: (1) Scale-down / SIGTERM — Kubernetes sends SIGTERM and waits for
// terminationGracePeriodSeconds; this path runs. (2) OOMKill / SIGKILL — no SIGTERM, so this path does not run.
func handleGracefulShutdown(logger interface{}, httpServer *http.Server) error {
	log := logger.(interface {
		Info(...interface{})
		Infow(string, ...interface{})
		Errorw(string, ...interface{})
	})

	// Set up graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("🛑 Starting graceful shutdown...")

	// Step 1: Mark shutting down so /ready returns 503 and Kubernetes removes pod from Service endpoints (traffic goes to other pods)
	shutdown.SetShuttingDown()
	readinessDrainSec := commoninit.GetConfigInt(constants.ServerShutdownReadinessDrainSeconds, constants.DefaultShutdownReadinessDrainSeconds)
	if readinessDrainSec > 0 {
		log.Infow("Waiting for readiness probe to fail so traffic is moved to other pods", "seconds", readinessDrainSec)
		time.Sleep(time.Duration(readinessDrainSec) * time.Second)
	}

	// Step 2: Drain in-flight HTTP requests (configurable timeout)
	httpDrainSec := commoninit.GetConfigInt(constants.ServerShutdownHTTPDrainSeconds, constants.DefaultShutdownHTTPDrainSeconds)
	if httpDrainSec < 1 {
		httpDrainSec = constants.DefaultShutdownHTTPDrainSeconds
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(httpDrainSec)*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Errorw("Error during server shutdown", "error", err)
	}

	// Step 3: Shutdown common modules (async ESA log queue, Mongo, cache) with bounded deadline
	modulesSec := commoninit.GetConfigInt(constants.ServerShutdownModulesTotalSeconds, constants.DefaultShutdownModulesTotalSeconds)
	if modulesSec < 1 {
		modulesSec = constants.DefaultShutdownModulesTotalSeconds
	}
	modulesCtx, modulesCancel := context.WithTimeout(context.Background(), time.Duration(modulesSec)*time.Second)
	defer modulesCancel()
	commoninit.Shutdown(modulesCtx)

	log.Info("✅ Graceful shutdown completed")
	return nil
}

// safeExecute wraps any function execution with panic recovery
func safeExecute(operation string, fn func() error, logger interface{}) error {
	log := logger.(interface {
		Errorw(string, ...interface{})
	})

	defer func() {
		if r := recover(); r != nil {
			log.Errorw("Operation panicked",
				"operation", operation,
				"panic", r,
				"stackTrace", string(debug.Stack()))
		}
	}()

	if err := fn(); err != nil {
		log.Errorw("Operation failed", "operation", operation, "error", err)
		return fmt.Errorf("%s failed: %v", operation, err)
	}

	return nil
}

// performEmergencyShutdown attempts to clean up resources during emergency shutdown
func performEmergencyShutdown() {
	defer func() {
		// Even emergency shutdown shouldn't panic
		if r := recover(); r != nil {
			log.Printf("🚨 Emergency shutdown itself panicked: %v", r)
		}
	}()

	log.Printf("🛑 Performing emergency shutdown...")

	// Try to shutdown common modules if they were initialized
	if commoninit.GetLogger() != nil {
		emergencySec := commoninit.GetConfigInt(constants.ServerShutdownEmergencySeconds, constants.DefaultShutdownEmergencySeconds)
		if emergencySec < 1 {
			emergencySec = constants.DefaultShutdownEmergencySeconds
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(emergencySec)*time.Second)
		defer cancel()
		commoninit.Shutdown(ctx)
	}

	log.Printf("✅ Emergency shutdown completed")
}

// initializeDatabases handles database initialization with proper error handling
func initializeDatabases(logger interface{}) error {
	log := logger.(interface {
		Info(...interface{})
		Errorw(string, ...interface{})
	})

	// Initialize SQL database
	log.Info("📊 Initializing SQL database...")
	if err := db.Init(); err != nil {
		return fmt.Errorf("SQL database initialization failed: %v", err)
	}

	// Check if database connection was successful
	log.Info("🔍 Initializing QueryObjectRepository...")
	dbService := db.DBService{}
	sqlDB := dbService.GetDB()

	if sqlDB == nil {
		return fmt.Errorf("SQL database connection is nil - database initialization failed")
	}

	// Get the underlying *sql.DB from GORM with error handling
	sqlConnection, err := sqlDB.DB()
	if err != nil {
		return fmt.Errorf("failed to get SQL connection for QueryObjectRepository: %v", err)
	}

	repository.InitQueryObjectRepository(sqlConnection)
	log.Info("✅ QueryObjectRepository initialized successfully")

	// Initialize MongoDB (optional component)
	log.Info("🍃 Initializing MongoDB...")
	if err := safeExecute("MongoDB initialization", func() error {
		ctx := context.Background()
		db.InitMongoDB(ctx)
		return nil
	}, logger); err != nil {
		log.Errorw("MongoDB initialization failed, but continuing", "error", err)
		// MongoDB is optional, so we don't fail the entire startup
	} else {
		log.Info("✅ MongoDB initialized successfully")
	}

	return nil
}
