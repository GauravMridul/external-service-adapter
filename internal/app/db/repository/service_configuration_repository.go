package repository

import (
	"context"
	"errors"
	"esa/internal/app/db"
	commoninit "esa/internal/app/init"
	"esa/internal/app/models/esa_models"
)

type IServiceConfigurationRepository interface {
	FindByServiceIdArray(ctx context.Context, serviceIds []int) ([]*esa_models.ServiceConfigurationResponse, error)
}

// ServiceConfigurationRepositoryImpl implements the service configuration repository
type ServiceConfigurationRepositoryImpl struct {
	DBService db.DBService
}

func NewServiceConfigurationRepositoryImpl() *ServiceConfigurationRepositoryImpl {
	repo := &ServiceConfigurationRepositoryImpl{
		DBService: db.DBService{},
	}
	return repo
}

func (r *ServiceConfigurationRepositoryImpl) FindByServiceIdArray(ctx context.Context, serviceIds []int) ([]*esa_models.ServiceConfigurationResponse, error) {
	log := commoninit.GetLogger(ctx)
	log.Info("Inside FindByServiceId method")

	var ServiceConfigurationResponseList []*esa_models.ServiceConfigurationResponse

	dbConnection := r.DBService.GetDB()
	if dbConnection == nil {
		log.Warn("DB connection is Null")
		return ServiceConfigurationResponseList, errors.New("db connection is null")
	}

	result := dbConnection.Select("id", "service_name", "api_url", "headers", "request_body", "request_method", "response_body", "send_response", "timeout", "additional_config").Where("id IN (?) AND is_deleted = false", serviceIds).Find(&ServiceConfigurationResponseList)
	if result.Error != nil {
		log.Errorw("Error fetching service configurations", "error", result.Error)
		return nil, result.Error
	}

	log.Infow("Successfully fetched service configurations",
		"count", len(ServiceConfigurationResponseList),
		"service_ids", serviceIds)

	return ServiceConfigurationResponseList, nil
}
