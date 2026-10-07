package repository

import (
	"context"
	"database/sql"
	commoninit "esa/internal/app/init"
	"esa/internal/app/models/esa_models"
	"fmt"
	"strings"
	"sync"
)

// IQueryObjectRepository defines the interface for query object repository operations
type IQueryObjectRepository interface {
	FindByObjectName(ctx context.Context, objectName string) (*esa_models.QueryObjectRelationshipMap, error)
	FindByObjectNames(ctx context.Context, objectNames []string) (map[string]*esa_models.QueryObjectRelationshipMap, error)
	FindByLabels(ctx context.Context, labels []string) (map[string]*esa_models.QueryObjectRelationshipMap, error)
}

// Singleton variables for QueryObjectRepository
var (
	instance     *QueryObjectRepository
	instanceOnce sync.Once
)

// QueryObjectRepository implements the IQueryObjectRepository interface
type QueryObjectRepository struct {
	db *sql.DB
}

// InitQueryObjectRepository initializes the global singleton instance of QueryObjectRepository
func InitQueryObjectRepository(db *sql.DB) {
	instanceOnce.Do(func() {
		instance = &QueryObjectRepository{
			db: db,
		}
		log := commoninit.GetLogger()
		log.Info("QueryObjectRepository singleton initialized")
	})
}

// GetQueryObjectRepository returns the global singleton instance of QueryObjectRepository
func GetQueryObjectRepository() IQueryObjectRepository {
	if instance == nil {
		log := commoninit.GetLogger()
		log.Error("QueryObjectRepository not initialized. Make sure to call InitQueryObjectRepository first.")
		panic("QueryObjectRepository not initialized")
	}
	return instance
}

// NewQueryObjectRepository creates a new instance of QueryObjectRepository - only use for tests or specific cases
func NewQueryObjectRepository(db *sql.DB) *QueryObjectRepository {
	return &QueryObjectRepository{
		db: db,
	}
}

// FindByObjectName retrieves a query object relationship map by object name (case-insensitive)
func (r *QueryObjectRepository) FindByObjectName(ctx context.Context, objectName string) (*esa_models.QueryObjectRelationshipMap, error) {
	log := commoninit.GetLogger(ctx)

	query := `
		SELECT id, query_object, query_relation, additional_fields, additional_conditions 
		FROM query_object_relationship_map 
		WHERE LOWER(query_object) = LOWER($1) AND is_deleted = false
	`

	var queryObject esa_models.QueryObjectRelationshipMap
	err := r.db.QueryRowContext(ctx, query, objectName).Scan(
		&queryObject.ID,
		&queryObject.QueryObject,
		&queryObject.QueryRelation,
		&queryObject.AdditionalFields,
		&queryObject.AdditionalConditions,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			log.Warnw("No query object relationship map found", "object", objectName)
			return nil, fmt.Errorf("no query object relationship map found for object: %s", objectName)
		}
		log.Errorw("Failed to query database", "error", err, "object", objectName)
		return nil, err
	}

	log.Debugw("Successfully found query object relationship", "object", objectName)
	return &queryObject, nil
}

// FindByObjectNames retrieves multiple query object relationship maps by object names in a single query (case-insensitive)
func (r *QueryObjectRepository) FindByObjectNames(ctx context.Context, objectNames []string) (map[string]*esa_models.QueryObjectRelationshipMap, error) {
	log := commoninit.GetLogger(ctx)

	if len(objectNames) == 0 {
		return make(map[string]*esa_models.QueryObjectRelationshipMap), nil
	}

	// Build the query with parameterized placeholders for the IN clause (case-insensitive)
	queryPlaceholders := make([]string, len(objectNames))
	queryArgs := make([]interface{}, len(objectNames))

	for i, name := range objectNames {
		queryPlaceholders[i] = fmt.Sprintf("LOWER($%d)", i+1)
		queryArgs[i] = strings.ToLower(name)
	}

	query := fmt.Sprintf(`
		SELECT id, query_object, query_relation, additional_fields, additional_conditions 
		FROM query_object_relationship_map 
		WHERE LOWER(query_object) IN (%s) AND is_deleted = false
		AND (object_label IS NULL OR object_label = '')
	`, strings.Join(queryPlaceholders, ","))

	rows, err := r.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		log.Errorw("Failed to query database for multiple objects", "error", err)
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]*esa_models.QueryObjectRelationshipMap)

	for rows.Next() {
		var obj esa_models.QueryObjectRelationshipMap
		if err := rows.Scan(
			&obj.ID,
			&obj.QueryObject,
			&obj.QueryRelation,
			&obj.AdditionalFields,
			&obj.AdditionalConditions,
		); err != nil {
			log.Errorw("Failed to scan database row", "error", err)
			return nil, err
		}

		// Store with lowercase key for consistent cache lookup
		result[strings.ToLower(obj.QueryObject)] = &obj
	}

	if err := rows.Err(); err != nil {
		log.Errorw("Error iterating over rows", "error", err)
		return nil, err
	}

	// Log any objects that weren't found
	for _, name := range objectNames {
		if _, exists := result[name]; !exists {
			log.Warnw("No query object relationship map found", "object", name)
		}
	}

	log.Infow("Successfully fetched query object relationships",
		"requested_count", len(objectNames),
		"found_count", len(result))

	return result, nil
}

// isValidLabelName checks if a label matches the pattern [A-Za-z_][A-Za-z0-9_]*
func isValidLabelName(label string) bool {
	if len(label) == 0 {
		return false
	}
	first := label[0]
	if !((first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z') || first == '_') {
		return false
	}
	for i := 1; i < len(label); i++ {
		c := label[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// FindByLabels retrieves query object relationship maps by their object_label values (case-insensitive).
// Returns a map keyed by lowercase label name.
func (r *QueryObjectRepository) FindByLabels(ctx context.Context, labels []string) (map[string]*esa_models.QueryObjectRelationshipMap, error) {
	log := commoninit.GetLogger(ctx)

	if len(labels) == 0 {
		return make(map[string]*esa_models.QueryObjectRelationshipMap), nil
	}

	// Validate and filter labels
	validLabels := make([]string, 0, len(labels))
	for _, label := range labels {
		if !isValidLabelName(label) {
			log.Errorw("Skipping invalid label name in FindByLabels", "label", label)
			continue
		}
		validLabels = append(validLabels, strings.ToLower(label))
	}

	if len(validLabels) == 0 {
		return make(map[string]*esa_models.QueryObjectRelationshipMap), nil
	}

	// Build parameterized query
	queryPlaceholders := make([]string, len(validLabels))
	queryArgs := make([]interface{}, len(validLabels))
	for i, label := range validLabels {
		queryPlaceholders[i] = fmt.Sprintf("LOWER($%d)", i+1)
		queryArgs[i] = label
	}

	query := fmt.Sprintf(`
		SELECT id, query_object, object_label, query_relation, additional_fields, additional_conditions
		FROM query_object_relationship_map
		WHERE LOWER(object_label) IN (%s) AND is_deleted = false
	`, strings.Join(queryPlaceholders, ","))

	rows, err := r.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		log.Errorw("Failed to query database for labels", "error", err)
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]*esa_models.QueryObjectRelationshipMap)

	for rows.Next() {
		var obj esa_models.QueryObjectRelationshipMap
		if err := rows.Scan(
			&obj.ID,
			&obj.QueryObject,
			&obj.ObjectLabel,
			&obj.QueryRelation,
			&obj.AdditionalFields,
			&obj.AdditionalConditions,
		); err != nil {
			log.Errorw("Failed to scan database row for label", "error", err)
			return nil, err
		}

		labelKey := strings.ToLower(obj.ObjectLabel.String)
		if _, exists := result[labelKey]; exists {
			log.Warnw("Duplicate object_label found (case-insensitive), using first match",
				"label", obj.ObjectLabel.String, "existingId", result[labelKey].ID, "duplicateId", obj.ID)
			continue
		}
		result[labelKey] = &obj
	}

	if err := rows.Err(); err != nil {
		log.Errorw("Error iterating over label rows", "error", err)
		return nil, err
	}

	// Log missing labels
	for _, label := range validLabels {
		if _, exists := result[label]; !exists {
			log.Warnw("No query object relationship map found for label", "label", label)
		}
	}

	log.Infow("Successfully fetched query object relationships by label",
		"requested_count", len(validLabels),
		"found_count", len(result))

	return result, nil
}
