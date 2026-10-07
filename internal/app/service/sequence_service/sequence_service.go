package sequence_service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"esa/internal/app/constants"
	"esa/internal/app/db/repository"
	"esa/internal/app/dto/common_dto"
	"esa/internal/app/dto/request_dto/esa_request_dto"
	commoninit "esa/internal/app/init"
	"esa/internal/app/models"
	"esa/internal/app/models/esa_models"
	"esa/internal/app/service/cache_service"
	"esa/internal/app/utility"
	"esa/pkg/client"

	"github.com/dmi-infotech/common-modules/go/contracts"
)

// Interface definition
type ISequenceService interface {
	ProcessSequenceWithMasterDTO(ctx context.Context, request *esa_request_dto.ProcessSequenceRequest) (*common_dto.MasterDTO, error)
	ExecuteSequence(ctx context.Context, request *esa_request_dto.ProcessSequenceRequest, masterDTO *common_dto.MasterDTO) error
}

// SequenceService struct - cleaned up with only essential fields
type SequenceService struct {
	ServiceConfigurationRepository repository.IServiceConfigurationRepository
	SalesforceClient               client.ISalesforceClient
	EsaLogRepository               repository.IEsaLogRepository
	CacheService                   *cache_service.CacheService
	logger                         contracts.Logger
	expressionProcessor            *ExpressionProcessor

	asyncEsaLogInitOnce      sync.Once
	asyncEsaLogCloseOnce     sync.Once
	asyncEsaLogQueue         chan asyncEsaLogTask
	asyncEsaLogTimeout       time.Duration
	asyncEsaLogInlineTimeout time.Duration  // per InsertMany for inline fallback (queue full, shutdown)
	asyncEsaLogShuttingDown  atomic.Uint32  // 0 = running, 1 = shutting down (no new enqueues)
	asyncEsaLogWg            sync.WaitGroup // so Close can wait for workers to exit

	// Count of audit-log batches that fell back to an inline (on-request-path) Mongo
	// insert because the async queue was full. Surfaced via runtime telemetry: a
	// non-zero and rising value means log backpressure is being paid as request latency.
	asyncEsaLogInlineFallback atomic.Uint64

	telemetryInitOnce sync.Once
}

// Constructor - unchanged
func NewSequenceService(
	serviceConfigurationRepository repository.IServiceConfigurationRepository,
	salesforceClient client.ISalesforceClient,
	esaLogRepository repository.IEsaLogRepository,
	cacheService *cache_service.CacheService,
) *SequenceService {
	svc := &SequenceService{
		ServiceConfigurationRepository: serviceConfigurationRepository,
		SalesforceClient:               salesforceClient,
		EsaLogRepository:               esaLogRepository,
		CacheService:                   cacheService,
		logger:                         commoninit.GetLogger(),
		expressionProcessor:            NewExpressionProcessor(),
	}
	svc.initAsyncEsaLogWriter()
	svc.initRuntimeTelemetry()
	return svc
}

type asyncEsaLogTask struct {
	ctx           context.Context
	requestLogger contracts.Logger
	logs          []*models.EsaLog
}

const (
	fanOutSourceWaitBuffer          = time.Duration(constants.DefaultFanOutSourceWaitBufferMs) * time.Millisecond
	fanOutSourceWaitHardCap         = time.Duration(constants.DefaultFanOutSourceWaitHardCapMs) * time.Millisecond
	fanOutSourcePollInterval        = time.Duration(constants.DefaultFanOutSourcePollIntervalMs) * time.Millisecond
	fanOutSourceProgressLogInterval = time.Duration(constants.DefaultFanOutSourceProgressLogIntervalMs) * time.Millisecond
)

func (s *SequenceService) initAsyncEsaLogWriter() {
	s.asyncEsaLogInitOnce.Do(func() {
		workerCount := commoninit.GetConfigInt(constants.AuditLogAsyncESAWorkersKey, constants.DefaultAsyncEsaLogWorkers)
		if workerCount < 1 {
			workerCount = constants.DefaultAsyncEsaLogWorkers
		}
		queueSize := commoninit.GetConfigInt(constants.AuditLogAsyncESAQueueSizeKey, constants.DefaultAsyncEsaLogQueueSize)
		if queueSize < 1 {
			queueSize = constants.DefaultAsyncEsaLogQueueSize
		}
		timeoutSeconds := commoninit.GetConfigInt(constants.AuditLogAsyncESATimeoutSecondsKey, constants.DefaultAsyncEsaLogTimeoutSeconds)
		if timeoutSeconds < 1 {
			timeoutSeconds = constants.DefaultAsyncEsaLogTimeoutSeconds
		}
		inlineMs := commoninit.GetConfigInt(constants.AuditLogAsyncESAInlineFallbackMsKey, constants.DefaultAsyncEsaLogInlineFallbackMs)
		if inlineMs < 1 {
			inlineMs = constants.DefaultAsyncEsaLogInlineFallbackMs
		}

		s.asyncEsaLogQueue = make(chan asyncEsaLogTask, queueSize)
		s.asyncEsaLogTimeout = time.Duration(timeoutSeconds) * time.Second
		s.asyncEsaLogInlineTimeout = time.Duration(inlineMs) * time.Millisecond

		for workerID := 1; workerID <= workerCount; workerID++ {
			s.asyncEsaLogWg.Add(1)
			go s.runAsyncEsaLogWorker(workerID)
		}

		if s.logger != nil {
			s.logger.Infow("Initialized async ESA log writer",
				"workers", workerCount, "queue_size", queueSize, "timeout_seconds", timeoutSeconds,
				"inline_fallback_ms", inlineMs)
		}
	})
}

func (s *SequenceService) runAsyncEsaLogWorker(workerID int) {
	defer s.asyncEsaLogWg.Done()
	for task := range s.asyncEsaLogQueue {
		baseCtx := task.ctx
		if baseCtx == nil {
			baseCtx = context.Background()
		}
		log := task.requestLogger
		if log == nil {
			log = s.logger.WithContext(baseCtx)
		}

		insertStart := time.Now()
		insertCtx, cancel := context.WithTimeout(context.WithoutCancel(baseCtx), s.asyncEsaLogTimeout)
		err := s.EsaLogRepository.InsertLogs(insertCtx, task.logs)
		cancel()
		durationMs := time.Since(insertStart).Milliseconds()

		if err != nil {
			log.Errorw("Failed to save ESA execution logs in bulk (async worker)", "error", err, "count", len(task.logs), "stage_duration_ms", durationMs, "worker_id", workerID)
			log.Infow("Checkpoint", "checkpoint", "mongo_logs_completed", "stage_duration_ms", durationMs, "count", len(task.logs), "status", "failed", "worker_id", workerID)
			continue
		}

		log.Infow("Checkpoint", "checkpoint", "mongo_logs_completed", "stage_duration_ms", durationMs, "count", len(task.logs), "status", "success", "worker_id", workerID)
	}
}

func (s *SequenceService) enqueueEsaLogs(ctx context.Context, requestLogger contracts.Logger, logs []*models.EsaLog) {
	if s.EsaLogRepository == nil || len(logs) == 0 {
		return
	}
	s.initAsyncEsaLogWriter()
	if s.asyncEsaLogQueue == nil {
		return
	}
	// During shutdown, do not send to channel (it may be closed); use inline insert so logs are not dropped
	if s.asyncEsaLogShuttingDown.Load() != 0 {
		s.insertEsaLogsInline(ctx, requestLogger, logs)
		return
	}

	task := asyncEsaLogTask{
		ctx:           ctx,
		requestLogger: requestLogger,
		logs:          logs,
	}
	select {
	case s.asyncEsaLogQueue <- task:
		return
	default:
		log := requestLogger
		if log == nil {
			log = s.logger.WithContext(ctx)
		}
		s.asyncEsaLogInlineFallback.Add(1)
		log.Warnw("Async ESA log queue full; trying inline fallback insert", "count", len(logs))
		s.insertEsaLogsInline(ctx, requestLogger, logs)
	}
}

// insertEsaLogsInline performs a synchronous insert of ESA logs (used when queue is full or during shutdown).
// Returns error if insert failed (caller can use this to log lead IDs for missed logs).
func (s *SequenceService) insertEsaLogsInline(ctx context.Context, requestLogger contracts.Logger, logs []*models.EsaLog) error {
	if s.EsaLogRepository == nil || len(logs) == 0 {
		return nil
	}
	log := requestLogger
	if log == nil {
		log = s.logger.WithContext(ctx)
	}
	baseCtx := ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	insertStart := time.Now()
	inlineTimeout := s.asyncEsaLogInlineTimeout
	if inlineTimeout <= 0 {
		inlineTimeout = time.Duration(constants.DefaultAsyncEsaLogInlineFallbackMs) * time.Millisecond
	}
	insertCtx, cancel := context.WithTimeout(context.WithoutCancel(baseCtx), inlineTimeout)
	err := s.EsaLogRepository.InsertLogs(insertCtx, logs)
	cancel()
	durationMs := time.Since(insertStart).Milliseconds()
	if err != nil {
		log.Errorw("Failed to save ESA execution logs in bulk (inline fallback)", "error", err, "count", len(logs), "stage_duration_ms", durationMs)
		return err
	}
	log.Infow("Checkpoint", "checkpoint", "mongo_logs_completed", "stage_duration_ms", durationMs, "count", len(logs), "status", "success_inline_fallback")
	return nil
}

// Close gracefully shuts down the async ESA log queue: stops new enqueues (they use inline insert),
// drains remaining tasks and attempts inline insert using each task's request logger (lead_id/correlation_id
// already in context), then closes the channel and waits for workers. Any persist failure is logged with
// enriched context by insertEsaLogsInline.
// Implements shutdown hook signature: func(ctx context.Context) error
func (s *SequenceService) Close(ctx context.Context) error {
	s.asyncEsaLogCloseOnce.Do(func() {
		if s.asyncEsaLogQueue == nil {
			return
		}
		s.asyncEsaLogShuttingDown.Store(1)

		drainSec := commoninit.GetConfigInt(constants.ServerShutdownAsyncLogDrainSeconds, constants.DefaultShutdownAsyncLogDrainSeconds)
		if drainSec < 1 {
			drainSec = constants.DefaultShutdownAsyncLogDrainSeconds
		}
		waitSec := commoninit.GetConfigInt(constants.ServerShutdownAsyncLogWaitSeconds, constants.DefaultShutdownAsyncLogWaitSeconds)
		if waitSec < 1 {
			waitSec = constants.DefaultShutdownAsyncLogWaitSeconds
		}
		drainTimeout := time.Duration(drainSec) * time.Second
		waitTimeout := time.Duration(waitSec) * time.Second

		// Drain queue: receive pending tasks and insert inline using each task's requestLogger (enriched with lead_id/correlation_id)
		drainCtx, drainCancel := context.WithTimeout(ctx, drainTimeout)
		var drained []asyncEsaLogTask
		for {
			select {
			case task, ok := <-s.asyncEsaLogQueue:
				if !ok {
					drainCancel()
					goto doneDrain
				}
				drained = append(drained, task)
			case <-drainCtx.Done():
				drainCancel()
				goto doneDrain
			}
		}
	doneDrain:

		if len(drained) > 0 && s.logger != nil {
			s.logger.Infow("Shutdown: drained async ESA log queue; attempting inline persist (failures logged with request lead_id/correlation_id)",
				"drained_task_count", len(drained))
		}
		insertedCount := 0
		for i, task := range drained {
			if ctx.Err() != nil {
				remaining := len(drained) - i
				firstID, lastID := "", ""
				if len(task.logs) > 0 {
					firstID = task.logs[0].SequenceId
					if cid := task.logs[0].CustomerID; cid != "" {
						firstID = cid + "/" + firstID
					}
				}
				if remaining > 1 {
					lastTask := drained[len(drained)-1]
					if len(lastTask.logs) > 0 {
						lastLog := lastTask.logs[len(lastTask.logs)-1]
						lastID = lastLog.SequenceId
						if cid := lastLog.CustomerID; cid != "" {
							lastID = cid + "/" + lastID
						}
					}
				}
				s.logger.Errorw("Shutdown: modules deadline exceeded; ESA log tasks not inserted",
					"remaining_task_count", remaining,
					"inserted_count", insertedCount,
					"first_identifier", firstID,
					"last_identifier", lastID)
				break
			}
			_ = s.insertEsaLogsInline(task.ctx, task.requestLogger, task.logs)
			insertedCount++
		}

		close(s.asyncEsaLogQueue)
		s.asyncEsaLogQueue = nil

		done := make(chan struct{})
		go func() {
			s.asyncEsaLogWg.Wait()
			close(done)
		}()
		waitCtx, waitCancel := context.WithTimeout(ctx, waitTimeout)
		defer waitCancel()
		select {
		case <-done:
			if s.logger != nil {
				s.logger.Info("Async ESA log queue closed; all workers exited")
			}
		case <-waitCtx.Done():
			if s.logger != nil {
				s.logger.Errorw("Async ESA log workers did not exit within timeout; shutdown proceeding (some logs may not have been persisted)",
					"timeout_seconds", int(waitTimeout.Seconds()))
			}
		}
	})
	return nil
}

// ProcessSequenceWithMasterDTO - core orchestration logic (unchanged)
func (s *SequenceService) ProcessSequenceWithMasterDTO(ctx context.Context, request *esa_request_dto.ProcessSequenceRequest) (*common_dto.MasterDTO, error) {
	log := s.getLog(ctx)
	requestStartTime := utility.GetRequestStartTime(ctx)
	if requestStartTime.IsZero() {
		requestStartTime = time.Now()
	}
	log.Info("Inside ProcessSequenceWithMasterDTO method")

	startTime := time.Now()

	// Create the MasterDTO instance
	masterDTO := &common_dto.MasterDTO{
		SequenceArray: utility.ParseNestedSequenceString(request.SequenceString),
		Data:          make(map[string]map[string]interface{}),
		EsaServices:   []*models.EsaLog{},
		Variables:     common_dto.NewVariableRegistry(),
	}

	// Extract all service IDs from the nested sequence array
	var allServiceIds []int
	for _, group := range masterDTO.SequenceArray {
		allServiceIds = append(allServiceIds, group...)
	}

	// Fetch service configurations with caching
	var serviceConfigs []*esa_models.ServiceConfigurationResponse
	var err error

	if s.CacheService != nil {
		serviceConfigs, err = s.CacheService.GetServiceConfigurations(ctx, allServiceIds)
		if err != nil {
			log.Errorw("Error getting service configurations from cache service", "error", err)
			return nil, err
		}
		log.Debugw("Service configurations retrieved via cache service", "count", len(serviceConfigs), "serviceIds", allServiceIds)
	} else {
		log.Debugw("Cache service not available, fetching service configurations directly from database")
		serviceConfigs, err = s.ServiceConfigurationRepository.FindByServiceIdArray(ctx, allServiceIds)
		if err != nil {
			log.Errorw("Error getting service configurations from database", "error", err)
			return nil, err
		}
	}

	// Initialize EsaServices array and ServiceMap
	masterDTO.EsaServices = make([]*models.EsaLog, 0, len(serviceConfigs))
	masterDTO.ServiceMap = make(map[int]*models.EsaLog, len(serviceConfigs))
	for _, config := range serviceConfigs {
		esaLog := &models.EsaLog{
			ServiceId:   int(config.ID),
			ServiceName: config.ServiceName,
			Request: models.RequestDetails{
				Method:  config.RequestMethod,
				URL:     config.APIURL,
				Headers: make(map[string]string),
				Body:    make(map[string]interface{}),
			},
			Response: models.ResponseDetails{
				Body: make(map[string]interface{}),
			},
			Status:                  "PENDING",
			CustomerID:              request.CustomerID,
			JourneyID:               request.SequenceID,
			ApplicationID:           request.ApplicationID,
			WorkFlowID:              request.WorkflowID,
			PartnerName:             request.PartnerName,
			ProgramName:             request.ProgramType,
			SalesChannelPartnerName: request.SalesChannelPartnerName,
			SourcingChannel:         request.SourcingChannel,
			NameOfConsolidator:      request.NameOfConsolidator,
			Stage:                   request.Stage,
			SequenceId:              request.SequenceID,
			ExtraFields:             make(map[string]interface{}),
		}

		// Convert headers and request body from JSON
		if err := json.Unmarshal(config.Headers, &esaLog.Request.Headers); err != nil {
			log.Warnw("Failed to unmarshal headers", "serviceID", config.ID, "error", err)
		}
		if err := json.Unmarshal(config.RequestBody, &esaLog.Request.Body); err != nil {
			log.Warnw("Failed to unmarshal request body", "serviceID", config.ID, "error", err)
		}

		// Reserved service names collide with the variable placeholder namespaces (var/self).
		if isReservedServiceName(config.ServiceName) {
			log.Warnw("Service name collides with a reserved variable namespace (var/self); ((var..))/((self)) references may be ambiguous",
				"serviceID", config.ID, "serviceName", config.ServiceName)
		}

		// Store additional configuration
		if len(config.AdditionalConfig) > 0 {
			esaLog.SetField("additional_config", string(config.AdditionalConfig))
			// Aggregate any `variables` declared by this service into the sequence-level registry.
			s.collectVariableDefinitions(ctx, masterDTO, int(config.ID), config.ServiceName, config.AdditionalConfig)
		}
		esaLog.SetField("send_response", config.SendResponse)
		if len(config.ResponseBody) > 0 {
			var responseBodyMap map[string]interface{}
			if err := json.Unmarshal(config.ResponseBody, &responseBodyMap); err == nil {
				esaLog.SetField("response_body", responseBodyMap)
				// Immediately set the response body so it's available for subsequent services
				esaLog.Response.Body = responseBodyMap
				esaLog.Response.StatusCode = 200
				esaLog.Status = "COMPLETED"
				esaLog.TimeTaken = 0 // No execution time for hardcoded responses
				// NOTE: response.source is intentionally NOT stamped here. This pre-set body is
				// only a fallback/mock; the real source is decided at execution time — api_call
				// (live HTTP), static_response (pre-configured response actually used), or
				// pick_response_from (skip path). Stamping static_response at load caused SKIPPED
				// services whose pick did not resolve to show a misleading "static_response".
			}
		}
		if config.Timeout > 0 {
			esaLog.SetField("timeout", config.Timeout)
		}

		masterDTO.EsaServices = append(masterDTO.EsaServices, esaLog)
		masterDTO.ServiceMap[esaLog.ServiceId] = esaLog
	}

	log.Infow("Initialized EsaServices and ServiceMap", "count", len(masterDTO.EsaServices))

	metadataElapsed := time.Since(requestStartTime).Milliseconds()
	metadataDuration := time.Since(startTime).Milliseconds()
	log.Infow("Checkpoint", "checkpoint", "metadata_fetched", "elapsed_since_start_ms", metadataElapsed, "stage_duration_ms", metadataDuration)

	// Extract objects and conditions for Salesforce querying
	objectsWithFields, objectsWithConditions := utility.ExtractObjectsFieldsAndConditions(serviceConfigs)
	log.Infow("Extracted objects with fields", "objectsWithFields", objectsWithFields, "objectsWithConditions", objectsWithConditions)

	sfStart := time.Now()
	// Fetch data from Salesforce
	dbData, err := s.SalesforceClient.QuerySalesforceObjectsWithConditions(ctx, request.CustomerID, objectsWithFields, objectsWithConditions)
	if err != nil {
		log.Errorw("Failed to query Salesforce data", "error", err)
		return nil, err
	}

	sfElapsed := time.Since(requestStartTime).Milliseconds()
	sfDuration := time.Since(sfStart).Milliseconds()
	log.Infow("Checkpoint", "checkpoint", "sf_data_fetched", "elapsed_since_start_ms", sfElapsed, "stage_duration_ms", sfDuration)
	log.Infow("Retrieved Salesforce data", "refID", request.CustomerID, "objectCount", len(dbData))

	// Convert Salesforce data to expected format
	convertedData := make(map[string]map[string]interface{})
	for objName, objData := range dbData {
		if dataMap, ok := objData.(map[string]interface{}); ok {
			convertedData[objName] = dataMap
		} else if dataArray, ok := objData.([]interface{}); ok {
			convertedData[objName] = map[string]interface{}{
				"records": dataArray,
			}
		} else {
			convertedData[objName] = make(map[string]interface{})
		}
	}

	masterDTO.Data = convertedData

	// Compute variable dependency staging and resolve any variables that depend only on Salesforce
	// data / literals / expressions (and each other) before the first service group runs.
	s.computeVariableReadyGroups(masterDTO)
	s.resolveVariablesStage(ctx, masterDTO, -1)

	// Execute sequence
	if err = s.ExecuteSequence(ctx, request, masterDTO); err != nil {
		log.Errorw("Error executing sequence", "error", err)
	}

	// Report any variables that could never be resolved (missing dependency, referenced service that
	// did not run, or a dependency cycle).
	s.logUnresolvedVariables(ctx, masterDTO)

	// If the API deadline expired but no error was surfaced, still return DeadlineExceeded so the client gets 504 + diagnostics.
	if err == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = context.DeadlineExceeded
	}

	// Calculate execution metrics and set final fields
	executionTime := time.Since(startTime).Milliseconds()
	overallStatus := "COMPLETED"
	now := time.Now()

	// Check for EXIT termination or other statuses
	hasExitTermination := false
	for _, esaLog := range masterDTO.EsaServices {
		if esaLog.Status == "COMPLETED_WITH_EXIT" || esaLog.Status == "TERMINATED" {
			hasExitTermination = true
		}
		if esaLog.Status == "FAILED" {
			overallStatus = "FAILED"
		}
	}

	// Set overall status - EXIT termination takes precedence over COMPLETED (unless FAILED)
	if hasExitTermination && overallStatus != "FAILED" {
		overallStatus = "TERMINATED"
	}

	for _, esaLog := range masterDTO.EsaServices {
		esaLog.OverallStatus = overallStatus
		esaLog.TotalExecutionTime = executionTime
		esaLog.CreatedAt = now
		esaLog.UpdatedAt = now
		esaLog.IsDeleted = false
	}

	// Save execution logs (flatten: for fan-out services insert N per-call logs then the aggregate)
	// Async insert: fire-and-forget so response is not blocked on MongoDB
	if s.EsaLogRepository != nil && len(masterDTO.EsaServices) > 0 {
		toInsert := make([]*models.EsaLog, 0)
		for _, esaLog := range masterDTO.EsaServices {
			if raw := esaLog.GetField(fanOutCallLogsKey); raw != nil {
				if callLogs, ok := raw.([]*models.EsaLog); ok {
					for _, cl := range callLogs {
						cl.OverallStatus = overallStatus
						cl.TotalExecutionTime = executionTime
						cl.CreatedAt = now
						cl.UpdatedAt = now
						cl.IsDeleted = false
						toInsert = append(toInsert, cl)
					}
				}
			}
			toInsert = append(toInsert, esaLog)
		}
		mongoElapsed := time.Since(requestStartTime).Milliseconds()
		log.Infow("Checkpoint", "checkpoint", "mongo_logs_initiated", "elapsed_since_start_ms", mongoElapsed)

		auditLogSync := commoninit.GetConfigBool(constants.AuditLogSyncKey, false)
		if auditLogSync {
			if err := s.EsaLogRepository.InsertLogs(ctx, toInsert); err != nil {
				log.Errorw("Failed to save ESA execution logs in bulk (sync)", "error", err, "count", len(toInsert))
			} else {
				log.Infow("Successfully saved all ESA execution logs (sync)", "count", len(toInsert))
			}
		} else {
			logsCopy := make([]*models.EsaLog, len(toInsert))
			copy(logsCopy, toInsert)
			s.enqueueEsaLogs(ctx, log, logsCopy)
		}
	} else {
		log.Warnw("EsaLogRepository is nil - cannot save execution logs")
	}

	return masterDTO, err
}

// ExecuteSequence - core execution logic (unchanged)
func (s *SequenceService) ExecuteSequence(ctx context.Context, request *esa_request_dto.ProcessSequenceRequest, masterDTO *common_dto.MasterDTO) error {
	log := s.getLog(ctx)
	requestStartTime := utility.GetRequestStartTime(ctx)
	if requestStartTime.IsZero() {
		requestStartTime = time.Now()
	}
	log.Info("Inside ExecuteSequence method")

	// Shared flag to track if EXIT was triggered (for parallel services in same group)
	var exitTriggered bool
	var exitTriggerMutex sync.Mutex

	// Process each sub-array sequentially
	for groupIndex, serviceGroup := range masterDTO.SequenceArray {
		groupCtx := utility.SetGroupIndex(ctx, groupIndex)
		log.Infow("Processing service group", "groupIndex", groupIndex, "serviceIDs", serviceGroup)

		// Check if EXIT was already triggered in a previous group
		exitTriggerMutex.Lock()
		if exitTriggered {
			exitTriggerMutex.Unlock()
			// Mark all services in this and remaining groups as SKIPPED
			for futureGroupIndex := groupIndex; futureGroupIndex < len(masterDTO.SequenceArray); futureGroupIndex++ {
				for _, serviceID := range masterDTO.SequenceArray[futureGroupIndex] {
					if esaLog, exists := masterDTO.ServiceMap[serviceID]; exists {
						if esaLog.Status == "PENDING" {
							esaLog.Status = "SKIPPED"
							esaLog.EndTime = time.Now()
							esaLog.TimeTaken = 0
							log.Infow("Marked service as SKIPPED due to EXIT in previous group",
								"serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName, "groupIndex", futureGroupIndex)
						}
					}
				}
			}
			// Return nil - EXIT is a valid termination, not an error
			return nil
		}
		exitTriggerMutex.Unlock()

		var wg sync.WaitGroup
		errChan := make(chan error, len(serviceGroup))

		maxServicesPerGroup := commoninit.GetConfigInt(constants.ServerMaxServicesPerGroup, constants.DefaultMaxServicesPerGroup)
		if maxServicesPerGroup < 1 {
			maxServicesPerGroup = 1
		}
		groupSem := make(chan struct{}, maxServicesPerGroup)

		// Execute services in this group in parallel (capped by max_services_per_group)
		for _, serviceID := range serviceGroup {
			esaLog, exists := masterDTO.ServiceMap[serviceID]
			if !exists {
				log.Warnw("Service ID not found in MasterDTO", "serviceID", serviceID)
				continue
			}

			wg.Add(1)
			esaLogCopy := esaLog
			commoninit.StartTrackedGoroutine(func() {
				esaLog := esaLogCopy
				defer wg.Done()
				groupSem <- struct{}{}        // acquire
				defer func() { <-groupSem }() // release
				defer func() {
					if r := recover(); r != nil {
						stackTrace := debug.Stack()
						log.Errorw("Service goroutine panic recovered",
							"serviceID", esaLog.ServiceId,
							"serviceName", esaLog.ServiceName,
							"panic", r,
							"stackTrace", string(stackTrace))

						esaLog.Status = "FAILED"
						esaLog.EndTime = time.Now()
						if esaLog.Response.Body == nil {
							esaLog.Response.Body = make(map[string]interface{})
						}
						esaLog.Response.Body["error"] = "Service execution failed due to internal error"
						esaLog.Response.Body["panic_details"] = fmt.Sprintf("%v", r)
						esaLog.Response.StatusCode = 500

						errChan <- fmt.Errorf("service panic in %s (ID: %d): %v", esaLog.ServiceName, esaLog.ServiceId, r)
					}
				}()

				// plug_response_into: wrap the final response body per config. No-op unless the
				// service configures plug_response_into AND the response source is allow-listed.
				// Registered here so it runs on every normal return path (pick_response_from,
				// static/pre-configured response, live api_call, and fan-out aggregate).
				defer s.applyPlugResponseInto(ctx, esaLog, masterDTO)

				// Check if EXIT was triggered by another service in the same group
				exitTriggerMutex.Lock()
				if exitTriggered {
					exitTriggerMutex.Unlock()
					// Mark this service as SKIPPED if it hasn't started processing
					if esaLog.Status == "PENDING" {
						esaLog.Status = "SKIPPED"
						esaLog.EndTime = time.Now()
						esaLog.TimeTaken = 0
						log.Infow("Service skipped due to EXIT triggered by another service in same group",
							"serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName)
					}
					return
				}
				exitTriggerMutex.Unlock()

				now := time.Now()
				esaLog.StartTime = now
				esaLog.Status = "PROCESSING"

				// Execute PreExecution checks
				preDecision, preErr := s.processPreExecution(ctx, esaLog, masterDTO)
				if preErr != nil {
					log.Errorw("PreExecution error", "serviceID", esaLog.ServiceId, "error", preErr)
					esaLog.Status = "FAILED"
					errChan <- preErr
					return
				}

				// Handle PreExecution decision
				switch preDecision {
				case ExecutionContinue:
					log.Infow("PreExecution decision: CONTINUE - skipping service execution", "serviceID", esaLog.ServiceId)
					esaLog.Status = "SKIPPED"
					esaLog.EndTime = time.Now()
					esaLog.TimeTaken = float64(time.Since(now).Milliseconds())
					if objectName, condition, fieldName, ok := s.parsePickResponseFrom(esaLog); ok {
						// Resolve the picked value (supports <Object.Field>, <Object[N].Field>,
						// <Object[condition].Field>, and nested/relationship field paths). A string is
						// treated as JSON to parse; an object is used directly.
						setBody := false
						parseErr := ""
						pickedStrLen := 0
						switch v := s.resolvePickFieldValue(masterDTO, objectName, condition, fieldName).(type) {
						case string:
							if v != "" {
								pickedStrLen = len(v)
								var parsed interface{}
								// normalizeJSONNaN mirrors the live-HTTP path so a CRIF/Analytics payload
								// containing NaN still parses. A truncated payload cannot be recovered.
								if err := json.Unmarshal([]byte(normalizeJSONNaN(v)), &parsed); err == nil {
									esaLog.Response.Body = asResponseBodyMap(parsed)
									setBody = true
								} else {
									parseErr = err.Error()
								}
							}
						case map[string]interface{}:
							esaLog.Response.Body = v
							setBody = true
						}
						if setBody {
							esaLog.Response.StatusCode = http.StatusOK
							esaLog.Response.Source = responseSourcePickResponseFrom
							log.Infow("pick_response_from: skipped-service response set from Salesforce data",
								"serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName, "object", objectName, "field", fieldName)
						} else {
							// pick_response_from is configured but resolved no usable value. Discard any
							// pre-set/static body (a staging-only mock) so the skipped service propagates an
							// empty response — matching production, where a skipped service has no live API
							// response to fall back to.
							esaLog.Response.Body = map[string]interface{}{}
							esaLog.Response.StatusCode = 0
							esaLog.Response.Source = ""
							if parseErr != "" {
								// The field WAS retrieved but is not valid JSON — most commonly a value
								// truncated at the Salesforce long-text field limit. Surfaced distinctly so
								// this is diagnosable without guessing.
								log.Warnw("pick_response_from: picked field is not valid JSON (often a value truncated at the Salesforce long-text field limit); discarded, empty response propagated",
									"serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName,
									"object", objectName, "condition", condition, "field", fieldName,
									"valueLength", pickedStrLen, "error", parseErr)
							} else {
								log.Warnw("pick_response_from: configured but no usable value resolved; discarded pre-set/static response body (empty response propagated)",
									"serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName,
									"object", objectName, "condition", condition, "field", fieldName)
							}
						}
					}
					return
				case ExecutionExit:
					log.Infow("PreExecution decision: EXIT - terminating sequence execution", "serviceID", esaLog.ServiceId)
					esaLog.Status = "TERMINATED"
					esaLog.EndTime = time.Now()
					esaLog.TimeTaken = float64(time.Since(now).Milliseconds())
					// Set EXIT flag instead of sending error
					exitTriggerMutex.Lock()
					exitTriggered = true
					exitTriggerMutex.Unlock()
					return
				case ExecutionExecute:
					log.Infow("PreExecution decision: EXECUTE - proceeding with service execution", "serviceID", esaLog.ServiceId)
				}

				// Fan-out from array: run N parallel calls and collate
				if cfg := getFanOutConfigFromEsaLog(esaLog); cfg != nil {
					err := s.executeFanOut(groupCtx, request, masterDTO, esaLog, cfg, now)
					endTime := time.Now()
					esaLog.EndTime = endTime
					esaLog.TimeTaken = float64(endTime.Sub(now).Milliseconds())
					if err != nil {
						log.Errorw("Fan-out execution failed", "serviceID", esaLog.ServiceId, "error", err)
						esaLog.Status = "FAILED"
						// Do not terminate sequence: fall through to post-execution so config can EXIT/SKIP
					} else {
						if esaLog.Status == "" || esaLog.Status == "PENDING" || esaLog.Status == "PROCESSING" {
							esaLog.Status = "COMPLETED"
						}
						log.Infow("Fan-out service completed", "serviceID", esaLog.ServiceId, "status", esaLog.Status)
						return
					}
				} else {
					// Execute the service
					err := s.processInvoke(groupCtx, request, masterDTO, esaLog)

					endTime := time.Now()
					esaLog.EndTime = endTime
					esaLog.TimeTaken = float64(endTime.Sub(now).Milliseconds())

					if err != nil {
						log.Errorw("Service execution failed (e.g. timeout); will run post-execution for config-driven EXIT/SKIP", "serviceID", esaLog.ServiceId, "error", err)
						esaLog.Status = "FAILED"
						// Do not terminate sequence: run post-execution so config can EXIT/SKIP based on response/error
					}
				}

				// Execute PostExecution checks (always run so config can react to success or failure/timeout)
				postDecision, postErr := s.processPostExecution(ctx, esaLog, masterDTO)
				if postErr != nil {
					log.Errorw("PostExecution error", "serviceID", esaLog.ServiceId, "error", postErr)
					esaLog.Status = "FAILED"
					errChan <- postErr
					return
				}

				// Handle PostExecution decision
				switch postDecision {
				case ExecutionContinue:
					log.Infow("PostExecution decision: CONTINUE - service completed normally", "serviceID", esaLog.ServiceId)
					if esaLog.Status != "FAILED" {
						esaLog.Status = "COMPLETED"
					}
				case ExecutionExit:
					log.Infow("PostExecution decision: EXIT - terminating sequence execution", "serviceID", esaLog.ServiceId)
					esaLog.Status = "COMPLETED_WITH_EXIT"
					// Set EXIT flag instead of sending error
					exitTriggerMutex.Lock()
					exitTriggered = true
					exitTriggerMutex.Unlock()
					return
				case ExecutionExecute:
					log.Infow("PostExecution decision: EXECUTE - service completed normally", "serviceID", esaLog.ServiceId)
					if esaLog.Status != "FAILED" {
						esaLog.Status = "COMPLETED"
					}
				}

				log.Infow("Service execution completed",
					"serviceID", esaLog.ServiceId,
					"serviceName", esaLog.ServiceName,
					"status", esaLog.Status,
					"executionTime", esaLog.TimeTaken)
			})
		}

		wg.Wait()
		close(errChan)

		// Check for actual errors (not EXIT)
		for err := range errChan {
			if err != nil {
				log.Errorw("Error executing service in group", "groupIndex", groupIndex, "error", err)
				// Only return actual errors, not EXIT terminations
				if !strings.Contains(err.Error(), "sequence execution terminated") {
					return err
				}
			}
		}

		groupElapsed := time.Since(requestStartTime).Milliseconds()
		log.Infow("Checkpoint", "checkpoint", "group_completed", "group_index", groupIndex, "group_label", fmt.Sprintf("group_%d", groupIndex+1), "elapsed_since_start_ms", groupElapsed)

		// Resolve any variables whose referenced services became terminal with this group, so they are
		// available to later groups' request bodies, headers, URLs, and pre/post-execution expressions.
		s.resolveVariablesStage(ctx, masterDTO, groupIndex)

		// Check if EXIT was triggered in this group
		exitTriggerMutex.Lock()
		if exitTriggered {
			exitTriggerMutex.Unlock()
			log.Infow("EXIT decision triggered, marking remaining service groups as SKIPPED",
				"completedGroups", groupIndex+1, "totalGroups", len(masterDTO.SequenceArray))

			// Mark all services in future groups as SKIPPED
			for futureGroupIndex := groupIndex + 1; futureGroupIndex < len(masterDTO.SequenceArray); futureGroupIndex++ {
				for _, serviceID := range masterDTO.SequenceArray[futureGroupIndex] {
					if esaLog, exists := masterDTO.ServiceMap[serviceID]; exists {
						if esaLog.Status == "PENDING" {
							esaLog.Status = "SKIPPED"
							esaLog.EndTime = time.Now()
							esaLog.TimeTaken = 0
							log.Infow("Marked future service as SKIPPED due to EXIT",
								"serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName, "groupIndex", futureGroupIndex)
						}
					}
				}
			}

			// Return nil - EXIT is a valid termination, not an error
			return nil
		}
		exitTriggerMutex.Unlock()
	}

	allGroupsElapsed := time.Since(requestStartTime).Milliseconds()
	log.Infow("Checkpoint", "checkpoint", "all_groups_completed", "elapsed_since_start_ms", allGroupsElapsed)
	log.Info("All service groups processed")
	return nil
}

// processInvoke - service invocation logic (unchanged)
func (s *SequenceService) processInvoke(ctx context.Context, request *esa_request_dto.ProcessSequenceRequest, masterDTO *common_dto.MasterDTO, esaLog *models.EsaLog) error {
	log := s.getLog(ctx)
	log.Infow("Executing service", "serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName)

	apiClient := commoninit.GetAPIClient()
	serviceInvoker := NewServiceInvoker(apiClient, s.expressionProcessor)

	return serviceInvoker.InvokeService(ctx, request, masterDTO, esaLog)
}

// executeFanOut runs N parallel calls from source array, collates responses, and sets aggregate + per-call logs on esaLog.
// At most fanOutMaxConcurrent (5) HTTP calls run at once; other goroutines wait on a semaphore. The aggregate log's
// StartTime is set when fan-out execution begins (after source is ready); EndTime is set by the caller when this returns.
func (s *SequenceService) executeFanOut(ctx context.Context, _ *esa_request_dto.ProcessSequenceRequest, masterDTO *common_dto.MasterDTO, aggregateLog *models.EsaLog, cfg *FanOutFromArrayConfig, _ time.Time) error {
	log := s.getLog(ctx)
	apiClient := commoninit.GetAPIClient()
	invoker := NewServiceInvoker(apiClient, s.expressionProcessor)

	// Wait for source to be completed (same or earlier group)
	sourceLog := getSourceEsaLog(masterDTO, cfg.Source)
	outputKey := cfg.Collation.OutputKey
	if outputKey == "" {
		outputKey = "results"
	}
	if sourceLog == nil {
		log.Warnw("Fan-out source service not found", "serviceID", aggregateLog.ServiceId, "sourceID", cfg.Source.ServiceID, "sourceName", cfg.Source.ServiceName)
		if aggregateLog.ExtraFields == nil {
			aggregateLog.ExtraFields = make(map[string]interface{})
		}
		aggregateLog.ExtraFields["fan_out_resolution"] = "skipped_source_not_found"
		aggregateLog.ExtraFields["fan_out_source_service_id"] = cfg.Source.ServiceID
		aggregateLog.ExtraFields["fan_out_source_service_name"] = cfg.Source.ServiceName
		aggregateLog.Status = "SKIPPED"
		aggregateLog.Response.Body = map[string]interface{}{outputKey: []interface{}{}}
		return nil
	}

	if sourceLog.ServiceId == aggregateLog.ServiceId {
		err := fmt.Errorf("fan-out source cannot reference self serviceID=%d", aggregateLog.ServiceId)
		aggregateLog.Status = "FAILED"
		if aggregateLog.ExtraFields == nil {
			aggregateLog.ExtraFields = make(map[string]interface{})
		}
		aggregateLog.ExtraFields["fan_out_resolution"] = "failed_source_self_reference"
		aggregateLog.ExtraFields["fan_out_source_service_id"] = sourceLog.ServiceId
		aggregateLog.ExtraFields["fan_out_source_status"] = sourceLog.Status
		aggregateLog.Response.StatusCode = http.StatusBadRequest
		aggregateLog.Response.Body = map[string]interface{}{
			"error":         "Fan-out source validation failed",
			"error_details": err.Error(),
		}
		return err
	}

	sourceWaitTimeout := s.resolveFanOutSourceWaitTimeout(invoker, sourceLog)
	waitCtx, cancelWait := context.WithTimeout(ctx, sourceWaitTimeout)
	defer cancelWait()
	waitStart := time.Now()
	pollTicker := time.NewTicker(fanOutSourcePollInterval)
	defer pollTicker.Stop()
	progressTicker := time.NewTicker(fanOutSourceProgressLogInterval)
	defer progressTicker.Stop()

	for {
		sourceStatus := sourceLog.Status
		switch sourceStatus {
		case "COMPLETED":
			goto sourceReady
		case "FAILED", "SKIPPED", "TERMINATED", "COMPLETED_WITH_EXIT":
			aggregateLog.Status = "SKIPPED"
			if aggregateLog.ExtraFields == nil {
				aggregateLog.ExtraFields = make(map[string]interface{})
			}
			aggregateLog.ExtraFields["fan_out_resolution"] = "skipped_source_terminal"
			aggregateLog.ExtraFields["fan_out_source_service_id"] = sourceLog.ServiceId
			aggregateLog.ExtraFields["fan_out_source_status"] = sourceStatus
			aggregateLog.Response.StatusCode = http.StatusOK
			aggregateLog.Response.Body = map[string]interface{}{outputKey: []interface{}{}}
			log.Warnw("Skipping fan-out because source is not executable", "serviceID", aggregateLog.ServiceId, "sourceServiceID", sourceLog.ServiceId, "sourceStatus", sourceStatus)
			return nil
		}

		select {
		case <-waitCtx.Done():
			waitElapsed := time.Since(waitStart).Milliseconds()
			err := fmt.Errorf("fan-out source wait timed out after %dms (sourceServiceID=%d status=%s timeout=%s)", waitElapsed, sourceLog.ServiceId, sourceLog.Status, sourceWaitTimeout)
			aggregateLog.Status = "FAILED"
			if aggregateLog.ExtraFields == nil {
				aggregateLog.ExtraFields = make(map[string]interface{})
			}
			aggregateLog.ExtraFields["fan_out_resolution"] = "failed_source_wait_timeout"
			aggregateLog.ExtraFields["fan_out_source_service_id"] = sourceLog.ServiceId
			aggregateLog.ExtraFields["fan_out_source_status"] = sourceLog.Status
			aggregateLog.ExtraFields["fan_out_source_wait_timeout_ms"] = sourceWaitTimeout.Milliseconds()
			aggregateLog.Response.StatusCode = http.StatusGatewayTimeout
			aggregateLog.Response.Body = map[string]interface{}{
				"error":         "Fan-out source wait failed",
				"error_details": err.Error(),
			}
			return err
		case <-progressTicker.C:
			log.Infow("Fan-out waiting for source service", "serviceID", aggregateLog.ServiceId, "sourceServiceID", sourceLog.ServiceId, "sourceStatus", sourceStatus, "elapsed_wait_ms", time.Since(waitStart).Milliseconds(), "wait_timeout_ms", sourceWaitTimeout.Milliseconds())
		case <-pollTicker.C:
		}
	}

sourceReady:

	arr := getArrayFromSourceResponse(sourceLog, cfg.Source.ArrayPath)
	items := applyFanOutFilter(arr, cfg.Filter)
	if len(items) == 0 {
		aggregateLog.Response.Body = map[string]interface{}{outputKey: []interface{}{}}
		return nil
	}

	// Start time for the fan-out aggregate entry: when we actually start the N calls (after source ready).
	aggregateLog.StartTime = time.Now()

	fanOutCap := commoninit.GetConfigInt(constants.ServerFanOutMaxConcurrent, constants.DefaultFanOutMaxConcurrent)
	if fanOutCap < 1 {
		fanOutCap = 1
	}
	// Semaphore: at most fanOutCap fan-out HTTP calls at a time (config: server.fan_out_max_concurrent).
	sem := make(chan struct{}, fanOutCap)

	serviceNameMap := make(map[string]*models.EsaLog)
	for _, svc := range masterDTO.EsaServices {
		if svc.ServiceId != aggregateLog.ServiceId && shouldIncludeForServicePlaceholderResolution(svc) {
			serviceNameMap[svc.ServiceName] = svc
		}
	}
	addVarPseudoService(serviceNameMap, masterDTO)

	additionalConfig, _ := invoker.parseAdditionalConfig(aggregateLog)
	if additionalConfig == nil {
		additionalConfig = &AdditionalConfig{}
	}
	httpClient, _ := invoker.createHTTPClientWithCertificates(ctx, additionalConfig)
	if httpClient == nil {
		httpClient = &http.Client{}
	}

	var wg sync.WaitGroup
	callLogs := make([]*models.EsaLog, 0, len(items))
	var callLogsMu sync.Mutex
	var collated []interface{}
	var collatedMu sync.Mutex
	totalCalls := len(items)
	for idx, it := range items {
		item, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		wg.Add(1)
		index, totalCount, callItem := idx, totalCalls, item
		commoninit.StartTrackedGoroutine(func() {
			defer wg.Done()
			sem <- struct{}{}        // acquire: cap at fanOutMaxConcurrent
			defer func() { <-sem }() // release
			perCall := &models.EsaLog{
				ServiceId:               aggregateLog.ServiceId,
				ServiceName:             aggregateLog.ServiceName,
				CustomerID:              aggregateLog.CustomerID,
				JourneyID:               aggregateLog.JourneyID,
				ApplicationID:           aggregateLog.ApplicationID,
				WorkFlowID:              aggregateLog.WorkFlowID,
				PartnerName:             aggregateLog.PartnerName,
				ProgramName:             aggregateLog.ProgramName,
				SalesChannelPartnerName: aggregateLog.SalesChannelPartnerName,
				SourcingChannel:         aggregateLog.SourcingChannel,
				NameOfConsolidator:      aggregateLog.NameOfConsolidator,
				Stage:                   aggregateLog.Stage,
				SequenceId:              aggregateLog.SequenceId,
				Request: models.RequestDetails{
					Method:  aggregateLog.Request.Method,
					URL:     aggregateLog.Request.URL,
					Headers: copyStringMap(aggregateLog.Request.Headers),
					Body:    deepCopyMap(aggregateLog.Request.Body),
				},
				Response:    models.ResponseDetails{Body: make(map[string]interface{})},
				Status:      "PENDING",
				ExtraFields: make(map[string]interface{}),
			}
			// Stamp for Mongo: per-call role, index, and human-readable label (e.g. "ServiceName (1 of 3)").
			perCall.ExtraFields[fanOutRoleKey] = "per_call"
			perCall.ExtraFields[fanOutCallIndexKey] = index
			perCall.ExtraFields[fanOutLogLabelKey] = fmt.Sprintf("%s (%d of %d)", aggregateLog.ServiceName, index+1, totalCount)
			invoker.PrepareRequestForFanOutCall(ctx, perCall, masterDTO, serviceNameMap, callItem, cfg.ItemBinding, additionalConfig)
			perCall.StartTime = time.Now()
			statusCode, responseBody, err := invoker.ExecuteSingleFanOutCall(ctx, perCall, httpClient, additionalConfig)
			perCall.EndTime = time.Now()
			perCall.TimeTaken = float64(perCall.EndTime.Sub(perCall.StartTime).Milliseconds())
			if err != nil {
				perCall.Status = "FAILED"
				if perCall.Response.Body == nil {
					perCall.Response.Body = make(map[string]interface{})
				}
				perCall.Response.Body["error"] = err.Error()
				perCall.Response.StatusCode = 500
			} else {
				_ = invoker.processServiceResponse(ctx, perCall, responseBody, statusCode, additionalConfig, "", masterDTO)
				if statusCode >= 200 && statusCode < 300 {
					perCall.Status = "COMPLETED"
					collatedMu.Lock()
					if cfg.Collation.Merge == "path" && cfg.Collation.Path != "" {
						if perCall.Response.Body != nil {
							if v := getNestedValue(perCall.Response.Body, cfg.Collation.Path); v != nil {
								collated = append(collated, v)
							}
						}
					} else {
						collated = append(collated, perCall.Response.Body)
					}
					collatedMu.Unlock()
				} else {
					perCall.Status = "FAILED"
				}
			}
			callLogsMu.Lock()
			callLogs = append(callLogs, perCall)
			callLogsMu.Unlock()
		})
	}
	wg.Wait()

	aggregateLog.Response.Body = map[string]interface{}{outputKey: collated}
	aggregateLog.Response.StatusCode = 200
	if len(collated) > 0 {
		aggregateLog.Status = "COMPLETED"
	} else {
		aggregateLog.Status = "COMPLETED"
	}
	if aggregateLog.ExtraFields == nil {
		aggregateLog.ExtraFields = make(map[string]interface{})
	}
	aggregateLog.Response.Source = responseSourceFanOut
	aggregateLog.ExtraFields[fanOutCallLogsKey] = callLogs
	// Stamp for Mongo: aggregate role and human-readable label (serviceName unchanged).
	aggregateLog.ExtraFields[fanOutRoleKey] = "aggregate"
	aggregateLog.ExtraFields[fanOutLogLabelKey] = aggregateLog.ServiceName + " (merged)"
	// Downstream services that reference this service by name (e.g. <ServiceName.results>) resolve from
	// masterDTO.EsaServices; only the aggregate log is in EsaServices, so they always get the merged response.
	return nil
}

func (s *SequenceService) resolveFanOutSourceWaitTimeout(invoker *ServiceInvoker, sourceLog *models.EsaLog) time.Duration {
	if invoker == nil || sourceLog == nil {
		return 30*time.Second + fanOutSourceWaitBuffer
	}

	sourceTimeoutSeconds := invoker.getServiceTimeout(sourceLog)
	if sourceTimeoutSeconds < 1 {
		sourceTimeoutSeconds = 30
	}

	attempts := 1
	additionalConfig, err := invoker.parseAdditionalConfig(sourceLog)
	if err == nil && additionalConfig != nil && additionalConfig.TokenManagement != nil &&
		additionalConfig.TokenManagement.Enabled && additionalConfig.TokenManagement.RetryOn401 {
		attempts = additionalConfig.TokenManagement.MaxTokenRetries
		if attempts < 1 {
			attempts = 2
		}
	}

	waitTimeout := time.Duration(sourceTimeoutSeconds*attempts)*time.Second + fanOutSourceWaitBuffer
	if waitTimeout > fanOutSourceWaitHardCap {
		return fanOutSourceWaitHardCap
	}
	return waitTimeout
}

func copyStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func deepCopyMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return make(map[string]interface{})
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		return deepCopyMap(val)
	case []interface{}:
		arr := make([]interface{}, len(val))
		for i, e := range val {
			arr[i] = deepCopyValue(e)
		}
		return arr
	default:
		return v
	}
}

func getNestedValue(m map[string]interface{}, path string) interface{} {
	parts := strings.Split(path, ".")
	current := interface{}(m)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		mp, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		var found bool
		for k, v := range mp {
			if strings.EqualFold(k, part) {
				current = v
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return current
}

// ============================================================================
// DATABASE PROCESSING FUNCTIONS (Essential for DB operations)
// ============================================================================

// evaluateSimpleCondition - evaluates simple equality conditions
func (s *SequenceService) evaluateSimpleCondition(record map[string]interface{}, condition string) bool {
	condition = strings.Trim(strings.TrimSpace(condition), "()")

	if strings.Contains(condition, "==") {
		parts := strings.Split(condition, "==")
		if len(parts) == 2 {
			fieldName := strings.TrimSpace(parts[0])
			expectedValue := strings.Trim(strings.TrimSpace(parts[1]), "'\"")

			if actualValue, exists := record[fieldName]; exists {
				actualStr := s.convertToString(actualValue)
				return actualStr == expectedValue ||
					(expectedValue == "true" && actualStr == "true") ||
					(expectedValue == "false" && actualStr == "false")
			}
		}
	}
	return false
}

// evaluateComplexCondition - evaluates conditions with logical operators
func (s *SequenceService) evaluateComplexCondition(record map[string]interface{}, condition string) bool {
	// Handle && operator (higher precedence)
	andParts := strings.Split(condition, "&&")
	if len(andParts) > 1 {
		for _, part := range andParts {
			if !s.evaluateConditionPart(record, strings.TrimSpace(part)) {
				return false
			}
		}
		return true
	}

	// Handle || operator
	orParts := strings.Split(condition, "||")
	if len(orParts) > 1 {
		for _, part := range orParts {
			if s.evaluateConditionPart(record, strings.TrimSpace(part)) {
				return true
			}
		}
		return false
	}

	return s.evaluateSimpleCondition(record, condition)
}

// evaluateConditionPart - evaluates a single condition part with parentheses handling
func (s *SequenceService) evaluateConditionPart(record map[string]interface{}, conditionPart string) bool {
	conditionPart = strings.TrimSpace(conditionPart)
	if strings.HasPrefix(conditionPart, "(") && strings.HasSuffix(conditionPart, ")") {
		inner := conditionPart[1 : len(conditionPart)-1]
		return s.evaluateComplexCondition(record, inner)
	}
	return s.evaluateSimpleCondition(record, conditionPart)
}

// convertToString - simplified type conversion (essential for DB operations)
func (s *SequenceService) convertToString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		if jsonBytes, err := json.Marshal(v); err == nil {
			jsonStr := string(jsonBytes)
			if strings.HasPrefix(jsonStr, "\"") && strings.HasSuffix(jsonStr, "\"") {
				return jsonStr[1 : len(jsonStr)-1]
			}
			return jsonStr
		}
		return fmt.Sprintf("%v", v)
	}
}

// ============================================================================
// PREEXECUTION AND POSTEXECUTION FUNCTIONS
// ============================================================================

// ExecutionDecision types
type ExecutionDecision string

const (
	ExecutionContinue ExecutionDecision = "CONTINUE"
	ExecutionExit     ExecutionDecision = "EXIT"
	ExecutionExecute  ExecutionDecision = "EXECUTE"
)

// parsePickResponseFrom reads PreExecution.pick_response_from from esaLog's additional_config.
// It supports both plain and conditional forms (with nested/relationship field paths):
//
//	<Object.Field>                         -> object, condition="", field
//	<Object.Rel__r.Field>                  -> object, condition="", field="Rel__r.Field"
//	<Object[N].Field>                      -> object, condition="N", field
//	<Object[Field == 'x'].Rel__r.Field>    -> object, condition="Field == 'x'", field="Rel__r.Field"
//
// Returns object name, condition (empty when none), field path, and ok=true when present and valid.
func (s *SequenceService) parsePickResponseFrom(esaLog *models.EsaLog) (objectName, condition, fieldName string, ok bool) {
	additionalConfigStr, exists := esaLog.GetField("additional_config").(string)
	if !exists || additionalConfigStr == "" {
		return "", "", "", false
	}
	var ac map[string]interface{}
	if err := json.Unmarshal([]byte(additionalConfigStr), &ac); err != nil {
		return "", "", "", false
	}
	pre, _ := ac["PreExecution"].(map[string]interface{})
	if pre == nil {
		pre, _ = ac["pre_execution"].(map[string]interface{})
	}
	if pre == nil {
		return "", "", "", false
	}
	pickStr, _ := pre["pick_response_from"].(string)
	pickStr = strings.TrimSpace(pickStr)
	if pickStr == "" {
		return "", "", "", false
	}
	// Strip the surrounding <...> then parse via the same helper used for <Object.Field>
	// placeholder resolution, so plain, indexed, and conditional forms are all recognised
	// (ObjectFieldPattern alone rejects an [condition] filter on the object).
	inner := pickStr
	if strings.HasPrefix(inner, "<") && strings.HasSuffix(inner, ">") {
		inner = inner[1 : len(inner)-1]
	}
	obj, cond, field, _ := utility.ParseConditionalPlaceholder(inner)
	obj = strings.TrimSpace(obj)
	field = strings.TrimSpace(field)
	// Reject malformed expressions. A leftover bracket in the object name means the input was
	// a conditional with no trailing ".Field" (e.g. "<Object[cond]>"); ParseConditionalPlaceholder's
	// fallback then mis-splits it into a bracketed object + garbage field, so treat it as invalid.
	if obj == "" || field == "" || strings.ContainsAny(obj, "[]") {
		return "", "", "", false
	}
	return obj, cond, field, true
}

// resolvePickFieldValue resolves the raw value targeted by pick_response_from, supporting the
// same forms as parsePickResponseFrom. It selects the record (by condition/index, or records[0]
// when there is no condition) and navigates the (possibly nested/relationship) field path,
// returning the raw value (string, map, etc.) or nil when nothing usable is found.
func (s *SequenceService) resolvePickFieldValue(masterDTO *common_dto.MasterDTO, objectName, condition, fieldName string) interface{} {
	if masterDTO == nil || masterDTO.Data == nil {
		return nil
	}
	objData, exists := masterDTO.Data[strings.ToLower(strings.TrimSpace(objectName))]
	if !exists || objData == nil {
		return nil
	}
	// navigateNestedFieldRaw resolves both a simple flat field (single-segment path) and a
	// nested/relationship path (e.g. Multibureau__r.Response__c), case-insensitively.
	fieldParts := s.expressionProcessor.splitPathWithArrayAccess(strings.Split(strings.TrimSpace(fieldName), "."))

	var records []interface{}
	if recordsVal, hasRecords := objData["records"]; hasRecords {
		records, _ = recordsVal.([]interface{})
	}
	condition = strings.TrimSpace(condition)

	// No condition: use the single record (records[0], or the bare object map).
	if condition == "" {
		record := objData
		if len(records) > 0 {
			record, _ = records[0].(map[string]interface{})
		}
		if record == nil {
			return nil
		}
		return s.expressionProcessor.navigateNestedFieldRaw(record, fieldParts)
	}

	// Numeric index: <Object[N].Field>.
	if idx, isIndex := parseArrayIndex(condition); isIndex {
		if len(records) > 0 {
			if idx < 0 || idx >= len(records) {
				return nil
			}
			if rec, ok := records[idx].(map[string]interface{}); ok {
				return s.expressionProcessor.navigateNestedFieldRaw(rec, fieldParts)
			}
			return nil
		}
		if idx == 0 {
			return s.expressionProcessor.navigateNestedFieldRaw(objData, fieldParts)
		}
		return nil
	}

	// Filter condition: first matching record whose field resolves to a usable value. A match
	// whose field is nil/empty-string does NOT short-circuit — keep scanning subsequent matches
	// (mirrors conditional DB-field resolution), so a later populated record can still win.
	if len(records) > 0 {
		for _, r := range records {
			rec, ok := r.(map[string]interface{})
			if !ok {
				continue
			}
			if !s.expressionProcessor.evaluateConditionOnObject(rec, condition) {
				continue
			}
			v := s.expressionProcessor.navigateNestedFieldRaw(rec, fieldParts)
			if v == nil {
				continue
			}
			if str, isStr := v.(string); isStr && str == "" {
				continue
			}
			return v
		}
		return nil
	}
	// Single bare object with a condition.
	if s.expressionProcessor.evaluateConditionOnObject(objData, condition) {
		return s.expressionProcessor.navigateNestedFieldRaw(objData, fieldParts)
	}
	return nil
}

// asResponseBodyMap converts parsed JSON (object, array, or other) to map[string]interface{} for Response.Body.
func asResponseBodyMap(parsed interface{}) map[string]interface{} {
	if parsed == nil {
		return nil
	}
	if m, ok := parsed.(map[string]interface{}); ok {
		return m
	}
	if arr, ok := parsed.([]interface{}); ok {
		return map[string]interface{}{"data": arr}
	}
	return map[string]interface{}{"value": parsed}
}

// --- plug_response_into ------------------------------------------------------
//
// plug_response_into lets a service reshape its own Response.Body by embedding the
// actual response into a JSON template. The template's marker token (plugResponseMarker)
// is replaced by the current response body, and the result becomes the complete
// Response.Body. Because Response.Body is a JSON object, the template root must be an
// object. The feature is scoped by response source via an apply_to allow-list so a
// service can choose which producing paths get wrapped.

// Response source identifiers stamped on ResponseDetails.Source at each body write site.
// plug_response_into's apply_to list is matched against these. HTTP error/timeout/failure
// bodies are never stamped, so they are never wrapped.
const (
	responseSourceAPICall          = "api_call"
	responseSourceStaticResponse   = "static_response"
	responseSourcePickResponseFrom = "pick_response_from"
	responseSourceFanOut           = "fan_out"
)

// plugResponseMarker is the sentinel replaced by the service's actual response body as a
// JSON value (object/array/scalar). It is intentionally dot-free so it never collides with
// the <Object.Field> syntax used by pick_response_from / Salesforce field extraction.
const plugResponseMarker = "<Actual_response>"

// plugResponseMarkerJSON is the sentinel replaced by the service's actual response body
// serialized to a JSON *string* (stringified). Use this when a downstream consumer expects
// the value at that key to be a JSON string it will parse itself (e.g. CRIF raw_response),
// rather than an embedded JSON object. Referencing an embedded object via ((Service.key))
// yields Go map notation (not valid JSON); the stringified form resolves cleanly.
const plugResponseMarkerJSON = "<Actual_response_json>"

// defaultPlugApplyTo is used when plug_response_into omits apply_to: wrap every in-scope
// successful source (errors/timeouts are excluded because they are never stamped).
func defaultPlugApplyTo() map[string]bool {
	return map[string]bool{
		responseSourceAPICall:          true,
		responseSourceStaticResponse:   true,
		responseSourcePickResponseFrom: true,
		responseSourceFanOut:           true,
	}
}

// parsePlugApplyTo builds the allow-list set from an apply_to JSON array. Falls back to
// the default set when the value is missing/empty/invalid.
func parsePlugApplyTo(v interface{}) map[string]bool {
	arr, ok := v.([]interface{})
	if !ok {
		return defaultPlugApplyTo()
	}
	set := make(map[string]bool, len(arr))
	for _, it := range arr {
		if s, ok := it.(string); ok {
			if key := strings.ToLower(strings.TrimSpace(s)); key != "" {
				set[key] = true
			}
		}
	}
	if len(set) == 0 {
		return defaultPlugApplyTo()
	}
	return set
}

// parsePlugResponseInto reads plug_response_into from esaLog's additional_config.
// Supported forms:
//
//	"plug_response_into": { "custom_response_key": "<Actual_response>" }          // bare template
//	"plug_response_into": { "template": {...}, "apply_to": ["api_call", ...] }     // template + allow-list
//
// The structured form is detected by the presence of a "template" key. Returns the
// template, the allowed-source set, and ok=true when a usable template is present.
func (s *SequenceService) parsePlugResponseInto(esaLog *models.EsaLog) (template interface{}, applyTo map[string]bool, ok bool) {
	additionalConfigStr, exists := esaLog.GetField("additional_config").(string)
	if !exists || additionalConfigStr == "" {
		return nil, nil, false
	}
	var ac map[string]interface{}
	if err := json.Unmarshal([]byte(additionalConfigStr), &ac); err != nil {
		return nil, nil, false
	}
	// plug_response_into may sit at the root of additional_config OR nested inside
	// pre_execution / post_execution (next to pick_response_from). Root takes precedence;
	// otherwise fall back to the execution blocks (both casings).
	raw, exists := ac["plug_response_into"]
	if !exists || raw == nil {
		for _, key := range []string{"pre_execution", "PreExecution", "post_execution", "PostExecution"} {
			execMap, _ := ac[key].(map[string]interface{})
			if execMap == nil {
				continue
			}
			if v, has := execMap["plug_response_into"]; has && v != nil {
				raw = v
				exists = true
				break
			}
		}
	}
	if !exists || raw == nil {
		return nil, nil, false
	}

	template = raw
	applyTo = defaultPlugApplyTo()
	if m, isMap := raw.(map[string]interface{}); isMap {
		if t, hasTemplate := m["template"]; hasTemplate {
			template = t
			if at, hasApplyTo := m["apply_to"]; hasApplyTo {
				applyTo = parsePlugApplyTo(at)
			}
		}
	}
	if template == nil {
		return nil, nil, false
	}
	return template, applyTo, true
}

// applyPlugResponseInto wraps esaLog.Response.Body into the configured plug_response_into
// template when the response source is allow-listed. Runs as a deferred call on every
// normal return path; it is a safe no-op when the feature is not configured, the source
// is not allowed, or the template root is not a JSON object.
func (s *SequenceService) applyPlugResponseInto(ctx context.Context, esaLog *models.EsaLog, masterDTO *common_dto.MasterDTO) {
	if esaLog == nil {
		return
	}
	template, applyTo, ok := s.parsePlugResponseInto(esaLog)
	if !ok {
		return
	}
	source := esaLog.Response.Source
	if source == "" || !applyTo[source] {
		return
	}

	// Resolve ((var.NAME)) / ((ServiceName.path)) placeholders in the template before plugging in
	// the actual response. This only touches the (( )) delimiter, so the <Actual_response> /
	// <Actual_response_json> markers (a different delimiter) are never affected.
	if masterDTO != nil {
		serviceNameMap := make(map[string]*models.EsaLog)
		for _, svc := range masterDTO.EsaServices {
			if svc.ServiceId != esaLog.ServiceId && shouldIncludeForServicePlaceholderResolution(svc) {
				serviceNameMap[svc.ServiceName] = svc
			}
		}
		addVarPseudoService(serviceNameMap, masterDTO)
		if len(serviceNameMap) > 0 {
			template = s.resolveVarsInPlugTemplate(template, serviceNameMap)
		}
	}

	// The value plugged into the template is the current response body (may be nil).
	var responseValue interface{} = esaLog.Response.Body

	wrapped := plugWalk(template, responseValue)
	m, isMap := wrapped.(map[string]interface{})
	if !isMap {
		// Response.Body must be a JSON object; skip wrapping if the template root is not one.
		s.getLog(ctx).Warnw("plug_response_into: template root is not a JSON object, skipping wrap",
			"serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName, "source", source)
		return
	}
	esaLog.Response.Body = m
	s.getLog(ctx).Infow("plug_response_into: response body wrapped",
		"serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName, "source", source)
}

// plugWalk returns a deep copy of template with every occurrence of plugResponseMarker
// replaced by responseValue. A string equal to the marker is replaced by the value itself
// (object/array/scalar); a marker embedded within a larger string is replaced by the
// JSON-stringified value.
func plugWalk(node interface{}, responseValue interface{}) interface{} {
	switch v := node.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for k, val := range v {
			out[k] = plugWalk(val, responseValue)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, val := range v {
			out[i] = plugWalk(val, responseValue)
		}
		return out
	case string:
		// Exact stringify token: value becomes the JSON-serialized response (a string).
		if v == plugResponseMarkerJSON {
			return stringifyForPlug(responseValue)
		}
		// Exact object token: value becomes the response itself (object/array/scalar).
		if v == plugResponseMarker {
			return responseValue
		}
		// Marker embedded within a larger string: substitute the stringified response.
		if strings.Contains(v, plugResponseMarkerJSON) {
			v = strings.ReplaceAll(v, plugResponseMarkerJSON, stringifyForPlug(responseValue))
		}
		if strings.Contains(v, plugResponseMarker) {
			v = strings.ReplaceAll(v, plugResponseMarker, stringifyForPlug(responseValue))
		}
		return v
	default:
		return v
	}
}

// stringifyForPlug renders a value as a string for marker substitution inside a larger string.
func stringifyForPlug(val interface{}) string {
	if val == nil {
		return ""
	}
	if b, err := json.Marshal(val); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", val)
}

// processPreExecution - evaluates PreExecution expressions
func (s *SequenceService) processPreExecution(ctx context.Context, esaLog *models.EsaLog, masterDTO *common_dto.MasterDTO) (ExecutionDecision, error) {
	log := s.getLog(ctx)

	additionalConfigStr, exists := esaLog.GetField("additional_config").(string)
	if !exists || additionalConfigStr == "" {
		log.Infow("PreExecution skipped: no additional_config", "serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName)
		return ExecutionExecute, nil
	}

	var additionalConfig map[string]interface{}
	if err := json.Unmarshal([]byte(additionalConfigStr), &additionalConfig); err != nil {
		log.Warnw("Failed to parse additional_config JSON", "serviceID", esaLog.ServiceId, "error", err)
		return ExecutionExecute, nil
	}

	preExecution, exists := additionalConfig["PreExecution"]
	if !exists {
		preExecution, exists = additionalConfig["pre_execution"]
	}
	if !exists {
		log.Infow("PreExecution skipped: no PreExecution key in additional_config", "serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName)
		return ExecutionExecute, nil
	}

	preExecutionMap, ok := preExecution.(map[string]interface{})
	if !ok {
		log.Warnw("PreExecution is not a valid object", "serviceID", esaLog.ServiceId)
		return ExecutionExecute, nil
	}

	// If enabled is explicitly false, skip PreExecution and proceed with execution
	if v, has := preExecutionMap["enabled"]; has {
		if b, ok := v.(bool); ok && !b {
			log.Infow("PreExecution skipped: enabled is false", "serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName)
			return ExecutionExecute, nil
		}
	}
	if v, has := preExecutionMap["Enabled"]; has {
		if b, ok := v.(bool); ok && !b {
			log.Infow("PreExecution skipped: Enabled is false", "serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName)
			return ExecutionExecute, nil
		}
	}

	log.Infow("Processing PreExecution", "serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName)

	decision, resolved := s.evaluateExecutionExpressions(ctx, esaLog, masterDTO, preExecutionMap)
	log.Infow("PreExecution decision", "serviceID", esaLog.ServiceId, "decision", decision)

	if len(resolved) > 0 {
		esaLog.SetField("resolved_pre_execution", resolved)
	}
	return decision, nil
}

// processPostExecution - evaluates PostExecution expressions
func (s *SequenceService) processPostExecution(ctx context.Context, esaLog *models.EsaLog, masterDTO *common_dto.MasterDTO) (ExecutionDecision, error) {
	log := s.getLog(ctx)

	additionalConfigStr, exists := esaLog.GetField("additional_config").(string)
	if !exists || additionalConfigStr == "" {
		return ExecutionExecute, nil
	}

	var additionalConfig map[string]interface{}
	if err := json.Unmarshal([]byte(additionalConfigStr), &additionalConfig); err != nil {
		log.Warnw("Failed to parse additional_config JSON", "serviceID", esaLog.ServiceId, "error", err)
		return ExecutionExecute, nil
	}

	postExecution, exists := additionalConfig["PostExecution"]
	if !exists {
		postExecution, exists = additionalConfig["post_execution"]
	}
	if !exists {
		return ExecutionExecute, nil
	}

	postExecutionMap, ok := postExecution.(map[string]interface{})
	if !ok {
		log.Warnw("PostExecution is not a valid object", "serviceID", esaLog.ServiceId)
		return ExecutionExecute, nil
	}

	// If enabled is explicitly false, skip PostExecution and proceed
	if v, has := postExecutionMap["enabled"]; has {
		if b, ok := v.(bool); ok && !b {
			log.Infow("PostExecution skipped: enabled is false", "serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName)
			return ExecutionExecute, nil
		}
	}
	if v, has := postExecutionMap["Enabled"]; has {
		if b, ok := v.(bool); ok && !b {
			log.Infow("PostExecution skipped: Enabled is false", "serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName)
			return ExecutionExecute, nil
		}
	}

	log.Infow("Processing PostExecution", "serviceID", esaLog.ServiceId, "serviceName", esaLog.ServiceName)

	decision, resolved := s.evaluateExecutionExpressions(ctx, esaLog, masterDTO, postExecutionMap)
	log.Infow("PostExecution decision", "serviceID", esaLog.ServiceId, "decision", decision)

	if len(resolved) > 0 {
		esaLog.SetField("resolved_post_execution", resolved)
	}
	return decision, nil
}

// validationsKey is the key for the array of validation expressions (supports both casing)
const validationsKey = "validations"

// maxLogExpressionLen truncates long expressions in logs so log lines are not dropped by size limits
const maxLogExpressionLen = 120

func truncateForLog(s string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = maxLogExpressionLen
	}
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// getLog returns the request-scoped logger (with lead_id, correlation_id, stage) when set in context,
// otherwise s.logger.WithContext(ctx). Using the request logger ensures pre/post-execution logs are
// visible with the same fields as other request logs and avoids redundant field stamping.
func (s *SequenceService) getLog(ctx context.Context) contracts.Logger {
	if reqLog := utility.GetRequestLogger(ctx); reqLog != nil {
		return reqLog
	}
	return s.logger.WithContext(ctx)
}

// evaluateExecutionExpressions - processes expressions in Pre/PostExecution (map entries + validations array).
// Returns the execution decision and a resolved map (key -> resolved string, or "validations" -> []resolved) for storage.
// Single pass: no extra loops; builds resolved map while evaluating.
func (s *SequenceService) evaluateExecutionExpressions(ctx context.Context, esaLog *models.EsaLog, masterDTO *common_dto.MasterDTO, executionMap map[string]interface{}) (ExecutionDecision, map[string]interface{}) {
	log := s.getLog(ctx)
	resolved := make(map[string]interface{})

	// Build service name map by status, not by EsaServices slice order, so ((ServiceName.path)) resolves correctly
	// regardless of config/DB order. Include all COMPLETED services (they have already run) plus current (for its own
	// PostExecution). O(n) one pass, O(n) map size — same as before; lookup remains O(1) by name.
	serviceNameMap := make(map[string]*models.EsaLog)
	for _, svc := range masterDTO.EsaServices {
		if shouldIncludeForServicePlaceholderResolution(svc) {
			serviceNameMap[svc.ServiceName] = svc
		}
	}
	if esaLog != nil && esaLog.ServiceName != "" {
		serviceNameMap[esaLog.ServiceName] = esaLog
	}
	addVarPseudoService(serviceNameMap, masterDTO)

	placeholderCache := make(map[string]string)
	finalDecision := ExecutionExecute

	mergeDecision := func(decision ExecutionDecision) {
		if decision == ExecutionExit {
			finalDecision = ExecutionExit
		} else if decision == ExecutionContinue && finalDecision == ExecutionExecute {
			finalDecision = ExecutionContinue
		}
	}

	evalExpr := func(expr string) (string, ExecutionDecision) {
		result := s.expressionProcessor.ProcessPlaceholdersCtx(ctx, expr, masterDTO, serviceNameMap, placeholderCache, nil)
		return result, s.parseExecutionDecision(result)
	}

	for key, value := range executionMap {
		keyLower := strings.ToLower(strings.TrimSpace(key))

		// Support validations array: key is "validations" / "Validations", value is []interface{}
		if (keyLower == validationsKey) && value != nil {
			arr, ok := value.([]interface{})
			if !ok {
				log.Infow("PreExecution validations key present but value is not an array; skipping", "serviceID", esaLog.ServiceId)
				continue
			}
			if len(arr) > 0 {
				log.Infow("PreExecution validations array", "serviceID", esaLog.ServiceId, "validationsCount", len(arr))
			}
			{
				vals := make([]interface{}, 0, len(arr))
				for i, item := range arr {
					str, okStr := item.(string)
					if !okStr {
						log.Debugw("Skipping validation item: not a string", "serviceID", esaLog.ServiceId, "validationsIndex", i)
						continue
					}
					if !strings.Contains(str, "{{") || !strings.Contains(str, "}}") {
						log.Debugw("Skipping validation item: no {{ }} expression", "serviceID", esaLog.ServiceId, "validationsIndex", i)
						continue
					}
					log.Infow("Evaluating validation expression", "serviceID", esaLog.ServiceId, "validationsIndex", i, "expression", truncateForLog(str, maxLogExpressionLen))
					result, decision := evalExpr(str)
					log.Infow("Validation result", "serviceID", esaLog.ServiceId, "validationsIndex", i, "result", result)
					vals = append(vals, result)
					mergeDecision(decision)
					if finalDecision == ExecutionExit {
						resolved[validationsKey] = vals
						return finalDecision, resolved
					}
				}
				if len(vals) > 0 {
					resolved[validationsKey] = vals
				}
			}
			continue
		}

		// Map entry: string value with expression
		valueStr, ok := value.(string)
		if !ok {
			continue
		}
		if !strings.Contains(valueStr, "{{") || !strings.Contains(valueStr, "}}") {
			continue
		}

		log.Infow("Evaluating expression", "serviceID", esaLog.ServiceId, "key", key, "expression", truncateForLog(valueStr, maxLogExpressionLen))
		result, decision := evalExpr(valueStr)
		log.Infow("Expression evaluation result", "serviceID", esaLog.ServiceId, "key", key, "result", result)
		resolved[key] = result
		mergeDecision(decision)
		if finalDecision == ExecutionExit {
			return finalDecision, resolved
		}
	}

	return finalDecision, resolved
}

// parseExecutionDecision - converts expression result to ExecutionDecision
func (s *SequenceService) parseExecutionDecision(result string) ExecutionDecision {
	result = strings.ToUpper(strings.TrimSpace(result))

	switch result {
	case "CONTINUE":
		return ExecutionContinue
	case "EXIT":
		return ExecutionExit
	case "EXECUTE":
		return ExecutionExecute
	case "TRUE", "1":
		return ExecutionExecute
	case "FALSE", "0":
		return ExecutionContinue
	default:
		return ExecutionExecute
	}
}

// shouldIncludeForServicePlaceholderResolution controls whether a service log is available for
// ((ServiceName.path)) placeholder resolution in downstream services.
// Include:
// - COMPLETED services (existing behavior)
// - SKIPPED services only when they carry a usable response body (e.g. pre_execution CONTINUE with pick_response_from)
func shouldIncludeForServicePlaceholderResolution(svc *models.EsaLog) bool {
	if svc == nil || svc.ServiceName == "" {
		return false
	}
	if svc.Status == "COMPLETED" {
		return true
	}
	if svc.Status == "SKIPPED" && svc.Response.StatusCode == http.StatusOK && svc.Response.Body != nil && len(svc.Response.Body) > 0 {
		return true
	}
	return false
}

// ============================================================================
// SIMPLIFIED TYPE CONVERSION (Essential for compatibility)
// ============================================================================

// TypeConversionMode for flexible type handling
type TypeConversionMode string

const (
	ModeString TypeConversionMode = "string"
	ModeNumber TypeConversionMode = "number"
	ModeAuto   TypeConversionMode = "auto"
)

// ============================================================================
// UTILITY HELPER FUNCTIONS
// ============================================================================
