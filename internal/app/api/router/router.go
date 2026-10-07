package router

import (
	"esa/internal/app/constants"
	"esa/internal/app/controller"
	"esa/internal/app/db"
	"esa/internal/app/db/repository"
	"esa/internal/app/service/cache_service"
	"esa/internal/app/service/sequence_service"

	// "esa/internal/app/errorhandling/errors_mapping"
	// "esa/internal/app/service/module_level_services"
	// "esa/internal/app/service/service_clients"
	commoninit "esa/internal/app/init"
	"esa/internal/app/utility"
	"esa/middleware/auth"
	"esa/pkg/client"

	"github.com/dmi-infotech/common-modules/go/contracts"
	"github.com/go-playground/validator/v10"

	"strings"

	"github.com/google/uuid"

	"github.com/gin-gonic/gin"
	cors "github.com/rs/cors/wrapper/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

// @title Swagger UI
// @version 1.0
// @description Swagger UI for CJM Microservice.
// @termsOfService http://swagger.io/terms/

// @contact.name API Support
// @contact.url http://www.swagger.io/support
// @contact.email support@swagger.io

// @license.name Apache 2.0
// @license.url http://www.apache.org/licenses/LICENSE-2.0.html

// @BasePath /customer-journey-manager

/*
	Note: You have to run "swag init -g server/router.go" from go/src/cjm
		  after updating any annotation comments either here or in any api's
*/

// NewRouter :
func NewRouter() *gin.Engine {
	log := commoninit.GetLogger()
	log.Info("Initializing Router")

	// Initialize all required services and repositories
	requestValidatorUtility, requestValidator, apiClient, serviceConfigurationRepository := InitServicesInstance()

	// Initialize the sequence controller with all dependencies
	sequenceController, cacheController := InitControllerForRouter(requestValidatorUtility, requestValidator, apiClient, serviceConfigurationRepository)

	// Get environment using centralized config
	env := commoninit.GetConfigString("Environment", "dev")

	// Initialize the router
	ginRouter := InitRouterAndAddMetaDataInRouter(env, sequenceController, cacheController)

	log.Info("Router initialization completed")
	return ginRouter
}

func InitRouterAndAddMetaDataInRouter(env string, sequenceController *controller.SequenceController, cacheController *controller.CacheController) *gin.Engine {
	log := commoninit.GetLogger()
	router := gin.New()
	router.Use(gin.Logger())
	router.Use(gin.Recovery())
	router.Use(uuidInjectionMiddleware())

	// Add CORS middleware using rs/cors package
	allowedOrigins := commoninit.GetConfigString("AllowedOrigins", "http://localhost:3000,http://localhost:8080")
	log.Infow("CORS configured", "allowedOrigins", allowedOrigins)

	corsHandler := cors.New(cors.Options{
		AllowedOrigins: strings.Split(allowedOrigins, ","),
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Origin", "Content-Type", "Content-Length", "Accept-Encoding", "X-CSRF-Token", "Authorization", "accept", "origin", "Cache-Control", "X-Requested-With", "X-API-Key", "X-Correlation-Id"},
	})
	router.Use(corsHandler)

	health := new(controller.HealthController)
	router.GET(constants.HealthCheckPathUrl, health.Status)
	router.GET(constants.ReadyCheckPathUrl, health.Ready)

	// Add swagger documentation if not in production
	if env != constants.Production {
		swaggerUrl := commoninit.GetConfigString("DMIBaseUrl", "http://localhost:8080") + constants.ExternalServiceAdapter + constants.SwaggerDocJSON
		url := ginSwagger.URL(swaggerUrl)
		router.GET(constants.SwaggerPathUrl, ginSwagger.WrapHandler(swaggerFiles.Handler, url))
	}

	// Grouping of router path
	esa := router.Group(constants.ExternalServiceAdapter)
	{
		v1 := esa.Group(constants.VersionV1)
		{
			v1.Use(auth.AuthManager.AuthMiddleware())

			v1.POST("/process-sequence/v2", sequenceController.ProcessSequenceV2())
			cache := v1.Group("/cache")
			{
				cache.GET("/ping", cacheController.PingCache())
				cache.GET("/stats", cacheController.GetCacheStats())
				cache.GET("/value", cacheController.GetCacheValueByKey())
				cache.POST("/clear", cacheController.ClearCache)
			}
		}
	}
	return router
}

func InitServicesInstance() (*utility.RequestValidator, *validator.Validate, contracts.APIClient, *repository.ServiceConfigurationRepositoryImpl) {
	requestValidatorUtility := utility.NewRequestValidator()
	requestValidator := utility.NewValidator()
	apiClient := commoninit.GetAPIClient()
	serviceConfigurationRepository := repository.NewServiceConfigurationRepositoryImpl()
	// errorMap := errors_mapping.NewCustomStatusMap()
	// httpStatusMap := errors_mapping.NewHttpStatusMap()
	// customStatusMap := errors_mapping.NewCustomStatusMap()
	return requestValidatorUtility, requestValidator, apiClient, serviceConfigurationRepository
}

func InitControllerForRouter(requestValidatorUtility *utility.RequestValidator, requestValidator *validator.Validate, apiClient contracts.APIClient, serviceConfigurationRepository *repository.ServiceConfigurationRepositoryImpl) (*controller.SequenceController, *controller.CacheController) {
	// IMPORTANT: Initialize the Salesforce service first
	salesforceService := &client.SalesforceService{}
	salesforceService.InitializeSalesforceClient(nil)

	// Create a Salesforce client instance and get it as interface
	salesforceClient := client.ISalesforceClient(client.NewSalesforceClient())

	// Initialize EsaLogRepository
	esaLogRepository := repository.NewEsaLogRepository()

	// Initialize QueryObjectRepository to get the singleton instance
	queryObjectRepo := repository.GetQueryObjectRepository()

	// Get ESA cache configuration
	esaCacheConfig := commoninit.GetESACacheConfig()

	// Create cache service with repositories and ESA cache configuration
	cacheService := cache_service.NewCacheService(
		serviceConfigurationRepository,
		queryObjectRepo,
		esaCacheConfig,
	)

	// Set cache service in the Salesforce client
	if sfClient, ok := salesforceClient.(*client.SalesforceClient); ok {
		sfClient.SetCacheService(cacheService)
	}

	// Create the sequence service with all dependencies including cache service
	sequenceService := sequence_service.NewSequenceService(
		serviceConfigurationRepository,
		salesforceClient,
		esaLogRepository,
		cacheService,
	)

	// Register graceful shutdown hooks: drain async ESA log queue first, then disconnect Mongo
	if commoninit.IsShutdownManagerAvailable() {
		commoninit.GetShutdownManager().AddShutdownHook(sequenceService.Close)
		commoninit.GetShutdownManager().AddShutdownHook(db.DisconnectMongo)
	}

	sequenceController := controller.NewSequenceController(requestValidator, requestValidatorUtility, apiClient, sequenceService)
	cacheController := controller.NewCacheController(cacheService)

	return sequenceController, cacheController
}

func uuidInjectionMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		correlationId := c.GetHeader(constants.CorrelationId)
		if len(correlationId) == 0 {
			correlationID, _ := uuid.NewUUID()
			correlationId = correlationID.String()
			c.Request.Header.Set(constants.CorrelationId, correlationId)
		}
		c.Writer.Header().Set(constants.CorrelationId, correlationId)

		c.Next()
	}
}
