package common_dto

import (
	"encoding/json"
	"esa/internal/app/models"
)

// MasterDTO is the main structure for sequence execution
type MasterDTO struct {
	SequenceArray [][]int                           `json:"sequenceArray"`
	Data          map[string]map[string]interface{} `json:"data"`
	EsaServices   []*models.EsaLog                  `json:"esaServices"` // Direct use of EsaLog for optimization
	ServiceMap    map[int]*models.EsaLog            `json:"-"`           // Service ID to EsaLog mapping for performance
	Variables     *VariableRegistry                 `json:"-"`           // Sequence-level variable registry (definitions + resolved values)
}

// MarshalJSON implements custom JSON marshaling to exclude sequenceArray from API response
// and include the resolved variables bag (name -> resolved value) when any were declared.
func (m *MasterDTO) MarshalJSON() ([]byte, error) {
	// Create a filtered struct for API response (excludes sequenceArray)
	filteredDTO := struct {
		Data        map[string]map[string]interface{} `json:"data"`
		EsaServices []*models.EsaLog                  `json:"esaServices"`
		Variables   map[string]interface{}            `json:"variables,omitempty"`
	}{
		Data:        m.Data,
		EsaServices: m.EsaServices,
	}

	if m.Variables != nil {
		if snapshot := m.Variables.Snapshot(); len(snapshot) > 0 {
			filteredDTO.Variables = snapshot
		}
	}

	return json.Marshal(filteredDTO)
}
