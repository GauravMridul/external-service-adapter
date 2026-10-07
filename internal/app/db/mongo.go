package db

import (
	"context"
	"fmt"
	"time"

	"esa/internal/app/constants"
	commoninit "esa/internal/app/init"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var mongoClient *mongo.Client
var mongoDB *mongo.Database

// To initialize the MongoDB connection
func InitMongoDB(ctx context.Context) {
	log := commoninit.GetLogger()
	config := commoninit.GetConfig()

	// Debug: Log all configuration to see what's being loaded
	log.Info("=== MongoDB Configuration Debug ===")
	allConfig := config.GetAll()
	log.Infow("All loaded configuration keys", "config", allConfig)

	// Debug: Check specific mongo keys
	log.Info("MongoDB configuration values",
		"mongo.connectionString", config.GetString("mongo.connectionString"),
		"mongo.dbName", config.GetString("mongo.dbName"),
		"mongo.host", config.GetString("mongo.host"),
		"mongo.username", config.GetString("mongo.username"),
		"mongo.password", config.GetString("mongo.password") != "",
		"mongo.minPoolSize", config.GetInt("mongo.minPoolSize"),
		"mongo.maxPoolSize", config.GetInt("mongo.maxPoolSize"),
		"mongo.maxConnIdleTimeInMs", config.GetInt("mongo.maxConnIdleTimeInMs"),
		"mongo.collections.esaLog", config.GetString("mongo.collections.esaLog"))

	// Access nested mongo config from common modules config
	minPoolSize := config.GetInt("mongo.minPoolSize")
	if minPoolSize == 0 {
		minPoolSize = 5 // Default value
	}

	maxPoolSize := config.GetInt("mongo.maxPoolSize")
	if maxPoolSize == 0 {
		maxPoolSize = 10 // Default value
	}

	maxConnIdleTimeInMs := config.GetInt("mongo.maxConnIdleTimeInMs")
	if maxConnIdleTimeInMs == 0 {
		maxConnIdleTimeInMs = 60000 // Default value
	}

	connectionString := config.GetString("mongo.connectionString")
	dbName := config.GetString("mongo.dbName")

	// If no connection string provided, try to build one from individual components
	if connectionString == "" {
		dbUser := config.GetString("mongo.username")
		dbPassword := config.GetString("mongo.password")
		dbHost := config.GetString("mongo.host")

		if dbHost == "" {
			log.Error("MongoDB configuration missing: no connectionString or host provided")
			return
		}

		// Create connection string based on whether auth is needed
		if dbUser != "" && dbPassword != "" {
			connectionString = fmt.Sprintf("mongodb://%s:%s@%s", dbUser, dbPassword, dbHost)
		} else {
			connectionString = fmt.Sprintf("mongodb://%s", dbHost)
		}
	}

	// Validate required fields
	if connectionString == "" {
		log.Error("MongoDB configuration missing: connectionString is empty")
		return
	}

	if dbName == "" {
		log.Error("MongoDB configuration missing: dbName is empty")
		return
	}

	log.Infow("Final MongoDB connection parameters",
		"connectionString", connectionString,
		"dbName", dbName,
		"minPoolSize", minPoolSize,
		"maxPoolSize", maxPoolSize,
		"maxConnIdleTimeInMs", maxConnIdleTimeInMs)

	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	connectionOptions := options.Client().ApplyURI(connectionString)
	connectionOptions.SetMinPoolSize(uint64(minPoolSize))
	connectionOptions.SetMaxPoolSize(uint64(maxPoolSize))
	connectionOptions.SetMaxConnIdleTime(time.Duration(maxConnIdleTimeInMs) * time.Millisecond)

	client, err := mongo.Connect(timeoutCtx, connectionOptions)
	if err != nil {
		log.Errorw("Unable to connect with MongoDB", "error", err)
		return
	}

	err = client.Ping(timeoutCtx, nil)
	if err != nil {
		log.Errorw("Failed to ping MongoDB server", "error", err)
		return
	}

	log.Info("Successfully connected to MongoDB!")
	mongoClient = client
	mongoDB = client.Database(dbName)
}

// GetMongoClient returns the MongoDB client
func GetMongoClient() *mongo.Client {
	return mongoClient
}

// GetMongoDB returns the MongoDB database
func GetMongoDB() *mongo.Database {
	return mongoDB
}

// DisconnectMongo disconnects the MongoDB client during graceful shutdown.
// It is safe to call if MongoDB was never initialized or already disconnected.
// Implements the shutdown hook signature: func(ctx context.Context) error
func DisconnectMongo(ctx context.Context) error {
	client := mongoClient
	mongoClient = nil
	mongoDB = nil
	if client == nil {
		return nil
	}
	log := commoninit.GetLogger()
	disconnectSec := commoninit.GetConfigInt(constants.ServerShutdownMongoDisconnectSeconds, constants.DefaultShutdownMongoDisconnectSeconds)
	if disconnectSec < 1 {
		disconnectSec = constants.DefaultShutdownMongoDisconnectSeconds
	}
	disconnectCtx, cancel := context.WithTimeout(ctx, time.Duration(disconnectSec)*time.Second)
	defer cancel()
	if err := client.Disconnect(disconnectCtx); err != nil {
		if log != nil {
			log.Errorw("MongoDB disconnect failed during shutdown", "error", err)
		}
		return fmt.Errorf("mongo disconnect: %w", err)
	}
	if log != nil {
		log.Info("MongoDB client disconnected successfully")
	}
	return nil
}
