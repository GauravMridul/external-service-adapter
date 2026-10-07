package utility

import (
	commoninit "esa/internal/app/init"

	"github.com/google/uuid"

	"github.com/go-playground/validator/v10"
)

// RequestValidatorUtil :
type IRequestValidatorUtil interface {
	IsValidRequest(leadId string, applicationId string, leadSourceId string) bool
	IsValidUUID(uuid string) bool
}

// RequestValidator :
type RequestValidator struct {
	Validator *validator.Validate
}

// NewRequestValidator :
func NewRequestValidator() *RequestValidator {
	return &RequestValidator{
		Validator: validator.New(),
	}
}

// IsValidRequest :
func (u RequestValidator) IsValidRequest(leadId string, applicationId string, leadSourceId string) bool {
	methodName := "IsValidRequest:"
	log := commoninit.GetLogger()
	log.Infow(methodName, " Validating the request for lead ", "leadId ", leadId, " applicationId ", applicationId, " and leadSourceId ", leadSourceId)
	if !(leadId == "") {
		return false
	}
	if !(applicationId == "") {
		return false
	}
	if !(leadSourceId == "") {
		return false
	}
	return true
}

func IsValidUUID(u string) bool {
	parsedUUID, err := uuid.Parse(u)
	return err == nil && parsedUUID != uuid.Nil
}

// Need to modified as per the requirement
func NewValidator() *validator.Validate {
	requestValidator := validator.New()
	log := commoninit.GetLogger()
	err := requestValidator.RegisterValidation("statusEnum", IsValidStatusEnumValue)
	if err != nil {
		log.Errorf("Error while registering custom validator func IsValidStatusEnumValue %s\n", err.Error())
	}
	err = requestValidator.RegisterValidation("requestContext", IsValidRequestContext)
	if err != nil {
		log.Errorf("Error while registering custom validator func IsValidRequestContext %s\n", err.Error())
	}
	return requestValidator
}

// Need to modified as per the requirement
func IsValidStatusEnumValue(fl validator.FieldLevel) bool {
	// status := fl.Field().Int()
	// if common_enums.Status(status).String() == constants.UnknownEnumValue {
	// 	return false
	// }
	return true
}

// Need to modified as per the requirement
func IsValidRequestContext(fl validator.FieldLevel) bool {
	// requestContext := fl.Field().String()
	// if common_enums.Parse(requestContext) == 0 {
	// 	return false
	// }
	return true
}

func (rv *RequestValidator) ValidateStruct(s interface{}) error {
	log := commoninit.GetLogger()

	err := rv.Validator.Struct(s)
	if err != nil {
		log.Errorw("Validation failed", "error", err)
		return err
	}

	log.Debug("Validation passed successfully")
	return nil
}

func (rv *RequestValidator) ValidateVar(field interface{}, tag string) error {
	log := commoninit.GetLogger()

	err := rv.Validator.Var(field, tag)
	if err != nil {
		log.Errorw("Variable validation failed", "error", err, "tag", tag)
		return err
	}

	log.Debugw("Variable validation passed", "tag", tag)
	return nil
}
