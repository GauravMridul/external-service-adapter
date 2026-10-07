package esa_request_dto

// ProcessSequenceRequest represents the request body for processing a sequence
type ProcessSequenceRequest struct {
	ApplicationID           string `json:"applicationId" binding:"required" example:"APP123456" description:"Partner application ID/Lead ID"`
	CustomerID              string `json:"customerId" binding:"required" example:"CUST123456" description:"Customer ID"`
	WorkflowID              string `json:"workflowId" binding:"omitempty"  example:"123e4567-e89b-12d3-a456-426614174000" description:"Workflow ID"`
	PartnerName             string `json:"partnerName" binding:"required" example:"Samsung"`
	ProgramType             string `json:"programType" example:"Personal Loan"`
	SalesChannelPartnerName string `json:"salesChannelPartnerName"`
	SourcingChannel         string `json:"sourcingChannel"`
	NameOfConsolidator      string `json:"nameOfConsolidator"`
	SequenceID              string `json:"sequenceId" binding:"required" example:"SEQ123456" description:"Sequence ID"`
	SequenceString          string `json:"sequenceString" binding:"required" example:"{1,2;5}" description:"Sequence String"`
	Stage                   string `json:"stage" binding:"required" example:"Decision" description:"Stage of the sequence execution"`
}
