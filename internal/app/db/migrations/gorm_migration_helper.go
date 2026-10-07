package db

import (
	commoninit "esa/internal/app/init"

	"gorm.io/gorm"
)

// GetAllModels returns all database models that need to be auto-migrated
func GetAllModels() []interface{} {
	return []interface{}{
		// &esa_models.ServiceConfiguration{},
		// &esa_models.QueryObjectRelationshipMap{},
		// Add more models here as they are created
	}
}

// RunAutoMigrations runs all GORM auto-migrations for the application models
func RunAutoMigrations(db *gorm.DB) error {
	log := commoninit.GetLogger()
	log.Info("Running GORM auto-migrations...")

	// Get all models to migrate
	models := GetAllModels()

	// Run auto-migration
	if err := db.AutoMigrate(models...); err != nil {
		log.Errorw("GORM auto-migration failed", "error", err.Error())
		return err
	}

	log.Info("GORM auto-migrations completed successfully")
	return nil
}
