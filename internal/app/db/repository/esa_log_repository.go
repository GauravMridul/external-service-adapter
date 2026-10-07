package repository

import (
	"context"
	"esa/internal/app/db"
	commoninit "esa/internal/app/init"
	"esa/internal/app/models"
	"esa/internal/app/utility"
	"fmt"
	"time"

	"github.com/dmi-infotech/common-modules/go/contracts"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// IEsaLogRepository defines the interface for ESA log repository operations
type IEsaLogRepository interface {
	InsertLog(ctx context.Context, esaLog *models.EsaLog) error
	InsertLogs(ctx context.Context, esaLogs []*models.EsaLog) error
	FindLogsByFilter(ctx context.Context, filter bson.M, limit int64, skip int64) ([]*models.EsaLog, error)
	UpdateLog(ctx context.Context, customerID string, update bson.M) error
	SoftDeleteLog(ctx context.Context, customerID string) error
	FindLogsByCustomerID(ctx context.Context, customerID string, limit int64, skip int64) ([]*models.EsaLog, error)
	FindLogsByJourneyID(ctx context.Context, journeyID string, limit int64, skip int64) ([]*models.EsaLog, error)
	FindLogsByApplicationID(ctx context.Context, applicationID string, limit int64, skip int64) ([]*models.EsaLog, error)
	FindLogsByPartnerName(ctx context.Context, partnerName string, limit int64, skip int64) ([]*models.EsaLog, error)
	FindLogsByStatus(ctx context.Context, status string, limit int64, skip int64) ([]*models.EsaLog, error)
	FindLogsByDateRange(ctx context.Context, startDate time.Time, endDate time.Time, limit int64, skip int64) ([]*models.EsaLog, error)
}

// EsaLogRepository implements the IEsaLogRepository interface
type EsaLogRepository struct {
	collection *mongo.Collection
	logger     contracts.Logger
}

// NewEsaLogRepository creates a new instance of EsaLogRepository
func NewEsaLogRepository() *EsaLogRepository {
	logger := commoninit.GetLogger()

	// Check if MongoDB is initialized
	mongoDB := db.GetMongoDB()
	if mongoDB == nil {
		logger.Fatal("MongoDB is not initialized - ESA logging is required for service operation")
	}

	collectionName := commoninit.GetConfigString("mongo.collections.esaLog", "esa-log")
	if collectionName == "esa-log" {
		logger.Warnw("Using default MongoDB collection name", "collection", collectionName)
	}

	collection := mongoDB.Collection(collectionName)

	return &EsaLogRepository{
		collection: collection,
		logger:     logger,
	}
}

// getLog returns the request-scoped logger when set in context, otherwise r.logger.WithContext(ctx).
func (r *EsaLogRepository) getLog(ctx context.Context) contracts.Logger {
	if reqLog := utility.GetRequestLogger(ctx); reqLog != nil {
		return reqLog
	}
	return r.logger.WithContext(ctx)
}

// InsertLog inserts a new ESA log record into the database
func (r *EsaLogRepository) InsertLog(ctx context.Context, esaLog *models.EsaLog) error {
	log := r.getLog(ctx)

	if esaLog.CreatedAt.IsZero() {
		esaLog.CreatedAt = time.Now()
	}

	_, err := r.collection.InsertOne(ctx, esaLog)
	if err != nil {
		log.Errorw("Failed to insert ESA log", "error", err)
		return err
	}

	return nil
}

// InsertLogs inserts multiple ESA log records into the database in a single bulk operation
func (r *EsaLogRepository) InsertLogs(ctx context.Context, esaLogs []*models.EsaLog) error {
	log := r.getLog(ctx)

	if len(esaLogs) == 0 {
		return nil // Nothing to insert
	}

	// Set creation time for any logs missing it
	now := time.Now()

	// Convert to []interface{} for MongoDB bulk insert
	documents := make([]interface{}, len(esaLogs))
	for i, esaLog := range esaLogs {
		if esaLog.CreatedAt.IsZero() {
			esaLog.CreatedAt = now
		}
		// Drop the redundant base-level "response_body" (a pre-configured mock kept in
		// ExtraFields, which is bson-inlined to the document root). It duplicates
		// response.body, so it is stripped before persistence. Runtime consumers of this
		// field (pre-configured response handling) run earlier, so removing it here is safe.
		if esaLog != nil && esaLog.ExtraFields != nil {
			delete(esaLog.ExtraFields, "response_body")
		}
		documents[i] = esaLog
	}

	// Perform bulk insert
	result, err := r.collection.InsertMany(ctx, documents)
	if err != nil {
		log.Errorw("Failed to bulk insert ESA logs", "error", err, "count", len(esaLogs))
		return err
	}

	log.Infow("Successfully inserted ESA logs in bulk", "count", len(result.InsertedIDs))
	return nil
}

// FindLogsByFilter retrieves ESA log records matching the provided filter
func (r *EsaLogRepository) FindLogsByFilter(ctx context.Context, filter bson.M, limit int64, skip int64) ([]*models.EsaLog, error) {
	log := r.getLog(ctx)

	// Default filter to exclude deleted logs
	if filter == nil {
		filter = bson.M{}
	}
	if _, exists := filter["isDeleted"]; !exists {
		filter["isDeleted"] = false
	}

	// Set up options for pagination
	findOptions := options.Find()
	if limit > 0 {
		findOptions.SetLimit(limit)
	}
	if skip > 0 {
		findOptions.SetSkip(skip)
	}

	// Sort by createdAt in descending order (newest first)
	findOptions.SetSort(bson.M{"createdAt": -1})

	cursor, err := r.collection.Find(ctx, filter, findOptions)
	if err != nil {
		log.Errorw("Error finding ESA logs with filter", "filter", filter, "error", err)
		return nil, err
	}
	defer cursor.Close(ctx)

	var logs []*models.EsaLog
	for cursor.Next(ctx) {
		var esaLog models.EsaLog
		if err := cursor.Decode(&esaLog); err != nil {
			log.Errorw("Error decoding ESA log", "error", err)
			return nil, err
		}
		logs = append(logs, &esaLog)
	}

	if err := cursor.Err(); err != nil {
		log.Errorw("Cursor error while finding ESA logs", "error", err)
		return nil, err
	}

	return logs, nil
}

// UpdateLog updates an existing ESA log record
func (r *EsaLogRepository) UpdateLog(ctx context.Context, customerID string, update bson.M) error {
	log := r.getLog(ctx)

	filter := bson.M{"customerId": customerID, "isDeleted": false}

	updateDoc := bson.M{"$set": update}

	result, err := r.collection.UpdateOne(ctx, filter, updateDoc)
	if err != nil {
		log.Errorw("Error updating ESA log", "customerId", customerID, "error", err)
		return err
	}

	if result.MatchedCount == 0 {
		log.Warnw("No ESA log found for customer", "customerId", customerID)
		return fmt.Errorf("no log found for customer %s", customerID)
	}

	return nil
}

// SoftDeleteLog marks an ESA log record as deleted by setting isDeleted to true
func (r *EsaLogRepository) SoftDeleteLog(ctx context.Context, customerID string) error {
	log := r.getLog(ctx)

	filter := bson.M{"customerId": customerID, "isDeleted": false}

	update := bson.M{"$set": bson.M{"isDeleted": true, "updatedAt": time.Now()}}

	result, err := r.collection.UpdateOne(ctx, filter, update)
	if err != nil {
		log.Errorw("Error soft-deleting ESA log", "customerId", customerID, "error", err)
		return err
	}

	if result.MatchedCount == 0 {
		log.Warnw("No ESA log found for customer for deletion", "customerId", customerID)
		return fmt.Errorf("no log found for customer %s", customerID)
	}

	return nil
}

// FindLogsByCustomerID retrieves ESA logs for a specific customer
func (r *EsaLogRepository) FindLogsByCustomerID(ctx context.Context, customerID string, limit int64, skip int64) ([]*models.EsaLog, error) {
	filter := bson.M{"customerId": customerID, "isDeleted": false}
	return r.FindLogsByFilter(ctx, filter, limit, skip)
}

// FindLogsByJourneyID retrieves ESA logs for a specific journey
func (r *EsaLogRepository) FindLogsByJourneyID(ctx context.Context, journeyID string, limit int64, skip int64) ([]*models.EsaLog, error) {
	filter := bson.M{"journeyId": journeyID, "isDeleted": false}
	return r.FindLogsByFilter(ctx, filter, limit, skip)
}

// FindLogsByApplicationID retrieves ESA logs for a specific application
func (r *EsaLogRepository) FindLogsByApplicationID(ctx context.Context, applicationID string, limit int64, skip int64) ([]*models.EsaLog, error) {
	filter := bson.M{"applicationId": applicationID, "isDeleted": false}
	return r.FindLogsByFilter(ctx, filter, limit, skip)
}

// FindLogsByPartnerName retrieves ESA logs for a specific partner
func (r *EsaLogRepository) FindLogsByPartnerName(ctx context.Context, partnerName string, limit int64, skip int64) ([]*models.EsaLog, error) {
	filter := bson.M{"partnerName": partnerName, "isDeleted": false}
	return r.FindLogsByFilter(ctx, filter, limit, skip)
}

// FindLogsByStatus retrieves ESA logs with a specific status
func (r *EsaLogRepository) FindLogsByStatus(ctx context.Context, status string, limit int64, skip int64) ([]*models.EsaLog, error) {
	filter := bson.M{"status": status, "isDeleted": false}
	return r.FindLogsByFilter(ctx, filter, limit, skip)
}

// FindLogsByDateRange retrieves ESA logs within a date range
func (r *EsaLogRepository) FindLogsByDateRange(ctx context.Context, startDate time.Time, endDate time.Time, limit int64, skip int64) ([]*models.EsaLog, error) {
	filter := bson.M{
		"createdAt": bson.M{
			"$gte": startDate,
			"$lte": endDate,
		},
		"isDeleted": false,
	}
	return r.FindLogsByFilter(ctx, filter, limit, skip)
}
