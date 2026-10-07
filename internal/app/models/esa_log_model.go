package models

import (
	"encoding/json"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// RequestDetails represents the structure of the request in EsaLog
type RequestDetails struct {
	Method  string                 `bson:"method" json:"method"`
	URL     string                 `bson:"url" json:"url"`
	Body    map[string]interface{} `bson:"body" json:"body"`
	Headers map[string]string      `bson:"headers" json:"headers"`
}

// ResponseDetails represents the structure of the response in EsaLog
type ResponseDetails struct {
	Body       map[string]interface{} `bson:"body" json:"body"`
	StatusCode int                    `bson:"statusCode" json:"statusCode"`
	// Source records which path produced Body (e.g. api_call, static_response,
	// pick_response_from, fan_out). It is internal control metadata used by
	// plug_response_into (apply_to) and is persisted inside the response object
	// rather than at the document root. Empty for error/timeout/failure bodies.
	Source string `bson:"source,omitempty" json:"source,omitempty"`
}

type EsaLog struct {
	ID                      primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	CustomerID              string             `bson:"customerId" json:"customerId"`
	JourneyID               string             `bson:"journeyId" json:"journeyId"`
	ApplicationID           string             `bson:"applicationId" json:"applicationId"`
	WorkFlowID              string             `bson:"workFlowId" json:"workFlowId"`
	Request                 RequestDetails     `bson:"request" json:"request"`
	Response                ResponseDetails    `bson:"response" json:"response"`
	Status                  string             `bson:"status" json:"status"`
	TimeTaken               float64            `bson:"timeTaken" json:"timeTaken"`
	PartnerName             string             `bson:"partnerName" json:"partnerName"`
	ProgramName             string             `bson:"programName" json:"programName"`
	SalesChannelPartnerName string             `bson:"salesChannelPartnerName" json:"salesChannelPartnerName"`
	SourcingChannel         string             `bson:"sourcingChannel" json:"sourcingChannel"`
	NameOfConsolidator      string             `bson:"nameOfConsolidator" json:"nameOfConsolidator"`
	Stage                   string             `bson:"stage" json:"stage"`
	CreatedAt               time.Time          `bson:"createdAt" json:"createdAt"`
	IsDeleted               bool               `bson:"isDeleted" json:"isDeleted"`
	UpdatedAt               time.Time          `bson:"updatedAt" json:"updatedAt"`
	ServiceId               int                `bson:"serviceId" json:"serviceId"`
	ServiceName             string             `bson:"serviceName" json:"serviceName"`
	SequenceId              string             `bson:"sequenceId" json:"sequenceId"`
	OverallStatus           string             `bson:"overallStatus" json:"overallStatus"`
	TotalExecutionTime      int64              `bson:"totalExecutionTimeMs" json:"totalExecutionTimeMs"`
	StartTime               time.Time          `bson:"startTime" json:"startTime"`
	EndTime                 time.Time          `bson:"endTime" json:"endTime"`
	// Additional fields can be added dynamically using the ExtraFields
	ExtraFields map[string]interface{} `bson:",inline" json:"-"`
}

// NewEsaLog creates a new EsaLog with default values
func NewEsaLog() *EsaLog {
	now := time.Now()
	return &EsaLog{
		Request: RequestDetails{
			Headers: make(map[string]string),
			Body:    make(map[string]interface{}),
		},
		Response: ResponseDetails{
			Body: make(map[string]interface{}),
		},
		IsDeleted:   false,
		CreatedAt:   now,
		UpdatedAt:   now,
		ExtraFields: make(map[string]interface{}),
	}
}

// SetField sets a field in the EsaLog, either in the predefined fields or in ExtraFields
func (log *EsaLog) SetField(key string, value interface{}) {
	log.ExtraFields[key] = value
}

// GetField gets a field value from either predefined fields or ExtraFields
func (log *EsaLog) GetField(key string) interface{} {
	if value, ok := log.ExtraFields[key]; ok {
		return value
	}

	// If field not found in structure
	return nil
}

// MarshalJSON implements custom JSON marshaling to filter response based on send_response flag
// and exclude internal fields from API response while keeping them in MongoDB
func (log *EsaLog) MarshalJSON() ([]byte, error) {
	// Create a filtered struct for API response (excludes internal fields)
	filteredLog := struct {
		ServiceId          int             `json:"serviceId"`
		ServiceName        string          `json:"serviceName"`
		Status             string          `json:"status"`
		TimeTaken          float64         `json:"timeTaken"`
		PartnerName        string          `json:"partnerName"`
		ProgramName        string          `json:"programName"`
		Stage              string          `json:"stage"`
		SequenceId         string          `json:"sequenceId"`
		OverallStatus      string          `json:"overallStatus"`
		TotalExecutionTime int64           `json:"totalExecutionTimeMs"`
		StartTime          string          `json:"startTime"`
		EndTime            string          `json:"endTime"`
		Response           ResponseDetails `json:"response"`
	}{
		ServiceId:          log.ServiceId,
		ServiceName:        log.ServiceName,
		Status:             log.Status,
		TimeTaken:          log.TimeTaken,
		PartnerName:        log.PartnerName,
		ProgramName:        log.ProgramName,
		Stage:              log.Stage,
		SequenceId:         log.SequenceId,
		OverallStatus:      log.OverallStatus,
		TotalExecutionTime: log.TotalExecutionTime,
		StartTime:          formatTime(log.StartTime),
		EndTime:            formatTime(log.EndTime),
		Response:           log.Response,
	}

	// Check send_response flag and filter response if needed
	if sendResponseField := log.GetField("send_response"); sendResponseField != nil {
		if sendResponse, ok := sendResponseField.(bool); ok && !sendResponse {
			// Create a filtered response with empty body but preserve status code
			filteredLog.Response = ResponseDetails{
				Body:       make(map[string]interface{}),
				StatusCode: log.Response.StatusCode,
			}
		}
	}

	return json.Marshal(filteredLog)
}

// formatTime formats a time.Time as RFC3339Nano string, or returns empty string for zero time
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}
