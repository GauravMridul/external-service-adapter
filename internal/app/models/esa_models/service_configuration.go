package esa_models

import (
	"time"

	"gorm.io/datatypes"
)

// ServiceConfiguration represents external service configuration
type ServiceConfiguration struct {
	ID               int64          `json:"id" gorm:"primaryKey;autoIncrement;type:bigint"`
	ServiceName      string         `json:"service_name" gorm:"type:varchar(255);not null"`
	APIURL           string         `json:"api_url" gorm:"type:varchar(512);not null;column:api_url"`
	Headers          datatypes.JSON `json:"headers" gorm:"type:jsonb;default:'{}'"`
	RequestBody      datatypes.JSON `json:"request_body" gorm:"type:jsonb;default:'{}'"`
	RequestMethod    string         `json:"request_method" gorm:"type:varchar(255);not null"`
	ResponseBody     datatypes.JSON `json:"response_body" gorm:"type:jsonb;default:'{}'"`
	SendResponse     bool           `json:"send_response" gorm:"default:false"`
	Timeout          int            `json:"timeout" gorm:"type:int4;default:0"`
	AdditionalConfig datatypes.JSON `json:"additional_config" gorm:"type:jsonb;default:'{}';column:additional_config"`
	CreatedDate      time.Time      `json:"created_date" gorm:"type:timestamp;not null"`
	CreatedBy        string         `json:"created_by" gorm:"type:varchar(255);not null"`
	ModifiedDate     time.Time      `json:"modified_date" gorm:"type:timestamp"`
	ModifiedBy       string         `json:"modified_by" gorm:"type:varchar(255)"`
	IsDeleted        bool           `json:"is_deleted" gorm:"default:false"`
}

type ServiceConfigurationResponse struct {
	ID               int64          `json:"id"`
	ServiceName      string         `json:"service_name"`
	APIURL           string         `json:"api_url"`
	Headers          datatypes.JSON `json:"headers"`
	RequestBody      datatypes.JSON `json:"request_body"`
	RequestMethod    string         `json:"request_method"`
	ResponseBody     datatypes.JSON `json:"response_body"`
	SendResponse     bool           `json:"send_response"`
	Timeout          int            `json:"timeout"`
	AdditionalConfig datatypes.JSON `json:"additional_config"`
}

// specifies the table name for ServiceConfiguration
func (ServiceConfiguration) TableName() string {
	return "service_configuration"
}

// specifies the table name for ServiceConfigurationResponse
func (ServiceConfigurationResponse) TableName() string {
	return "service_configuration"
}
