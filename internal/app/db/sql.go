// internal/app/db/sql.go
package db

import (
	"esa/internal/app/constants"
	abstraction "esa/internal/app/db/abstraction"
	migrations "esa/internal/app/db/migrations"
	commoninit "esa/internal/app/init"
	"fmt"
	"os"
	"sync"

	"gorm.io/gorm"
)

var (
	db          *gorm.DB
	err         error
	connectOnce sync.Once
)

type DBService struct{}

// Init initializes the database connection
func Init() error {
	log := commoninit.GetLogger()
	config := commoninit.GetConfig()

	log.Info("=== ENTERING DATABASE INITIALIZATION ===")

	// Debug: Check if config is available
	if config == nil {
		log.Error("=== CONFIG IS NIL ===")
		return fmt.Errorf("configuration is nil")
	}

	log.Info("=== CONFIG IS AVAILABLE ===")

	// Debug: Check what configuration keys are available
	allSettings := config.GetAll()
	log.Info("=== ALL CONFIG KEYS AVAILABLE ===", "totalKeys", len(allSettings))

	// Log all keys for debugging
	configKeys := make([]string, 0, len(allSettings))
	for k := range allSettings {
		configKeys = append(configKeys, k)
	}
	log.Info("Available configuration keys", "keys", configKeys)

	var initErr error
	if db == nil {
		connectOnce.Do(func() {
			log.Info("=== RETRIEVING DATABASE CONFIG VALUES ===")

			// Get individual config values with debug logging
			dbUsername := config.GetString("database.username")
			dbPassword := config.GetString("database.password")
			dbHost := config.GetString("database.host")
			dbName := config.GetString("database.name")
			dbPort := config.GetString("database.port")

			log.Info("=== RAW CONFIG VALUES ===",
				"database.username", dbUsername,
				"database.password", dbPassword != "",
				"database.host", dbHost,
				"database.name", dbName,
				"database.port", dbPort)

			// Also check if the keys exist at all
			log.Info("=== CONFIG KEY EXISTENCE CHECK ===",
				"database.username.exists", config.IsSet("database.username"),
				"database.password.exists", config.IsSet("database.password"),
				"database.host.exists", config.IsSet("database.host"),
				"database.name.exists", config.IsSet("database.name"),
				"database.port.exists", config.IsSet("database.port"))

			// Get database configuration from common modules config
			dbConfig := &abstraction.DatabaseConfig{
				Username:               dbUsername,
				Password:               dbPassword,
				Host:                   dbHost,
				DatabaseName:           dbName,
				Port:                   dbPort,
				MaxIdleConnections:     config.GetInt("database.maxIdleConnections"),
				MaxOpenConnections:     config.GetInt("database.maxOpenConnections"),
				ConnMaxLifetimeInHours: config.GetInt("database.connMaxLifetimeInHours"),
				Options:                make(map[string]string),
			}

			// Get the database dialect
			dialect := config.GetString("database.dialect")
			if dialect == "" {
				dialect = "postgres" // Default to postgres for backward compatibility
			}

			log.Info("=== DATABASE CONFIG CREATED ===",
				"dialect", dialect,
				"host", dbConfig.Host,
				"port", dbConfig.Port,
				"username", dbConfig.Username,
				"database", dbConfig.DatabaseName,
				"maxIdleConnections", dbConfig.MaxIdleConnections,
				"maxOpenConnections", dbConfig.MaxOpenConnections)

			// Validate required database configuration
			if dbHost == "" {
				initErr = fmt.Errorf("database host is empty")
				return
			}
			if dbUsername == "" {
				initErr = fmt.Errorf("database username is empty")
				return
			}
			if dbPassword == "" {
				initErr = fmt.Errorf("database password is empty")
				return
			}
			if dbName == "" {
				initErr = fmt.Errorf("database name is empty")
				return
			}

			// Get the database provider
			provider, err := abstraction.GetDatabaseProvider(dialect)
			if err != nil {
				log.Errorw("Failed to get database provider", "error", err)
				initErr = fmt.Errorf("failed to get database provider: %v", err)
				return
			}

			log.Infow("Database credentials being used",
				"host", dbConfig.Host,
				"port", dbConfig.Port,
				"username", dbConfig.Username,
				"database", dbConfig.DatabaseName,
				"password", dbConfig.Password != "",
			)

			// Connect to the database
			log.Infow("Attempting to connect to database", "host", dbConfig.Host, "database", dbConfig.DatabaseName)
			db, err = provider.Connect(dbConfig)
			if err != nil {
				log.Errorw("Failed to connect to database", "dialect", dialect, "host", dbConfig.Host, "error", err)
				initErr = fmt.Errorf("failed to connect to %s database at %s: %v", dialect, dbConfig.Host, err)
				return
			}

			log.Infow("Successfully connected to database", "dialect", provider.GetDialectName())

			// Run auto-migrations (can be enabled/disabled)
			shouldRunAutoMigrations := config.GetBool("database.shouldRunAutoMigrations")
			if shouldRunAutoMigrations {
				if err := RunGormAutoMigrations(provider, db); err != nil {
					log.Errorw("Auto-migration failed", "error", err)
					// Non-fatal in development
					if config.GetString("Environment") == constants.Production {
						initErr = fmt.Errorf("auto-migration failed in production: %v", err)
						return
					}
				}
			} else {
				log.Info("GORM auto-migrations disabled")
			}

			// Run Goose migrations (can be enabled/disabled)
			shouldRunGooseMigrations := config.GetBool("database.shouldRunGooseMigrations")
			if shouldRunGooseMigrations {
				if err := RunGooseMigrations(provider, db); err != nil {
					log.Errorw("Goose migration failed", "error", err)
					// Can be made non-fatal if needed
					if config.GetString("Environment") == constants.Production {
						initErr = fmt.Errorf("goose migration failed in production: %v", err)
						return
					}
				}
			} else {
				log.Info("Goose migrations disabled")
			}

		})
	}

	return initErr
}

// RunGormAutoMigrations handles GORM auto migrations
func RunGormAutoMigrations(provider abstraction.DatabaseProvider, db *gorm.DB) error {
	log := commoninit.GetLogger()
	log.Info("Running GORM auto-migrations...")

	if err := provider.RunGormAutoMigrations(db, migrations.GetAllModels()...); err != nil {
		log.Errorw("Auto-migration failed", "error", err.Error())
		return err
	}

	log.Info("Auto-migrations completed successfully")
	return nil
}

func RunGooseMigrations(provider abstraction.DatabaseProvider, db *gorm.DB) error {
	log := commoninit.GetLogger()

	// Get current working directory
	workingDir, err := os.Getwd()
	if err != nil {
		log.Errorw("Failed to get working directory", "error", err.Error())
		return err
	}

	migrationsPath := workingDir + constants.DbMigrationDir

	log.Infow("Using migrations path", "path", migrationsPath)

	// Run Goose migrations
	if err := provider.RunGooseMigrations(db, migrationsPath); err != nil {
		log.Errorw("Migration failed", "error", err.Error())
		return err
	}

	log.Info("Database migrations completed successfully")
	return nil
}

// GetDB returns the database connection
func (d DBService) GetDB() *gorm.DB {
	return db
}
