package controller

import (
	"context"
	"errors"
	"esa/internal/app/constants"
	"esa/internal/app/dto/request_dto/esa_request_dto"
	commoninit "esa/internal/app/init"
	"esa/internal/app/service/sequence_service"
	"esa/internal/app/utility"
	"esa/pkg/correlation"
	"net/http"
	"time"

	"github.com/dmi-infotech/common-modules/go/contracts"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

type SequenceController struct {
	Validator       *validator.Validate
	Utility         *utility.RequestValidator
	ApiClient       contracts.APIClient
	SequenceService sequence_service.ISequenceService
}

func NewSequenceController(validator *validator.Validate, requestValidator *utility.RequestValidator, apiClient contracts.APIClient, sequenceService sequence_service.ISequenceService) *SequenceController {
	controller := &SequenceController{
		Validator:       validator,
		Utility:         requestValidator,
		ApiClient:       apiClient,
		SequenceService: sequenceService,
	}
	return controller
}

// ProcessSequenceV2 handles the processing of a sequence with enhanced MasterDTO response
// @Summary Process sequence v2
// @Description Process a sequence of service calls with enhanced response structure
// @Tags ESA
// @Accept json
// @Produce json
// @Param request body esa_request_dto.ProcessSequenceRequest true "Process sequence request"
// @Security ApiKeyAuth
// @Success 200 {object} common_dto.MasterDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 500 {object} map[string]string
// @Router /external-service-adapter/v1/process-sequence/v2 [post]
func (s *SequenceController) ProcessSequenceV2() gin.HandlerFunc {
	fn := func(c *gin.Context) {
		ctx := correlation.WithReqContext(c)
		ctx = utility.AddIPHeadersToContext(c, ctx)

		log := commoninit.GetLogger(ctx)
		log.Info("Inside ProcessSequenceV2 controller method")

		token := c.GetHeader(constants.ApiKey)
		expectedToken := commoninit.GetConfigString(constants.ApiKey, "")
		if token != expectedToken {
			log.Warnw("Invalid API key provided", "provided_key", token, "expected_key", expectedToken)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
			return
		}

		var request esa_request_dto.ProcessSequenceRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			log.Errorw("Error binding request", "error", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		requestStartTime := time.Now()
		// correlation_id is already added by GetLogger(ctx) via Logger.WithContext(ctx) in common-modules; only add lead_id and stage
		reqLogger := log.WithFields(map[string]interface{}{
			"lead_id": request.CustomerID,
			"stage":   request.Stage,
		})
		ctx = utility.SetRequestStartTime(ctx, requestStartTime)
		ctx = utility.SetRequestLogger(ctx, reqLogger)

		reqLogger.Infow("Checkpoint", "checkpoint", "request_received", "elapsed_since_start_ms", 0, "stage_duration_ms", 0)

		if !commoninit.TryAcquireSequenceRequest() {
			log.Warnw("Sequence request rejected: max concurrent limit reached")
			c.Header("Retry-After", "10")
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error":   "Service temporarily overloaded; retry after Retry-After seconds",
				"message": "max concurrent sequence requests limit reached",
			})
			return
		}
		defer commoninit.ReleaseSequenceRequest()

		// Apply max processing timeout for the entire process-sequence API (configurable, default 120s)
		maxSec := commoninit.GetConfigInt(constants.ServerProcessSequenceMaxSeconds, constants.DefaultProcessSequenceMaxSeconds)
		if maxSec < 1 {
			maxSec = constants.DefaultProcessSequenceMaxSeconds
		}
		reqCtx, cancel := context.WithTimeout(ctx, time.Duration(maxSec)*time.Second)
		defer cancel()
		ctx = reqCtx

		// Process the sequence with MasterDTO
		masterDTO, err := s.SequenceService.ProcessSequenceWithMasterDTO(ctx, &request)
		if err != nil {
			log.Errorw("Error processing sequence", "error", err)
			if errors.Is(err, context.DeadlineExceeded) {
				elapsed := time.Since(requestStartTime).Milliseconds()
				diagnostics := sequence_service.BuildProcessSequenceTimeoutDiagnostics(masterDTO, ctx, elapsed, maxSec)
				// Logging does not depend on the expired request context; reqLogger carries lead_id / correlation for DM.
				reqLogger.Warnw("Process sequence timed out — diagnostic snapshot",
					"checkpoint", "process_sequence_timeout",
					"timeout_seconds", maxSec,
					"elapsed_ms", elapsed,
					"timeout_diagnostics", diagnostics)
				c.JSON(http.StatusGatewayTimeout, gin.H{
					"error":               "Process sequence timed out",
					"message":             "Request exceeded maximum processing time",
					"timeout_seconds":     maxSec,
					"elapsed_ms":          elapsed,
					"timeout_diagnostics": diagnostics,
				})
				return
			}

			// Create detailed error response
			errorResponse := gin.H{
				"error":     err.Error(),
				"timestamp": time.Now().UTC().Format(time.RFC3339),
			}

			// If masterDTO was partially created, include service details for debugging
			if masterDTO != nil && len(masterDTO.EsaServices) > 0 {
				failedServices := make([]gin.H, 0)
				for _, service := range masterDTO.EsaServices {
					if service.Status == "FAILED" {
						serviceError := gin.H{
							"service_id":   service.ServiceId,
							"service_name": service.ServiceName,
							"status":       service.Status,
						}

						// Include error details from response body if available
						if service.Response.Body != nil {
							if errorMsg, exists := service.Response.Body["error"]; exists {
								serviceError["error"] = errorMsg
							}
							if errorDetails, exists := service.Response.Body["error_details"]; exists {
								serviceError["error_details"] = errorDetails
							}
							if panicDetails, exists := service.Response.Body["panic_details"]; exists {
								serviceError["panic_details"] = panicDetails
							}
						}

						if service.Response.StatusCode > 0 {
							serviceError["status_code"] = service.Response.StatusCode
						}

						failedServices = append(failedServices, serviceError)
					}
				}

				if len(failedServices) > 0 {
					errorResponse["failed_services"] = failedServices
				}
			}

			elapsed := time.Since(requestStartTime).Milliseconds()
			reqLogger.Infow("Checkpoint", "checkpoint", "response_sent", "elapsed_since_start_ms", elapsed, "stage_duration_ms", elapsed)
			c.JSON(http.StatusInternalServerError, errorResponse)
			return
		}

		elapsed := time.Since(requestStartTime).Milliseconds()
		reqLogger.Infow("Checkpoint", "checkpoint", "response_sent", "elapsed_since_start_ms", elapsed, "stage_duration_ms", elapsed)
		c.JSON(http.StatusOK, masterDTO)
	}
	return fn
}
