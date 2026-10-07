# external-service-adapter
This service is for managing communication with all external vendor APIs &amp; services and also for executing sequence of processes defined as per architecture

# Using the MasterDTO for Sequence Processing

The application now provides an enhanced sequence processing endpoint that uses the MasterDTO structure (located in `internal/app/dto/common_dto`) for better organization and execution.

## MasterDTO

The MasterDTO is a centralized data structure that contains:

- `sequenceArray` - Array of service execution groups
- `data` - Map containing request data and execution results
- `services` - Array of service configurations with execution details

## Example Usage

```bash
curl -X POST http://localhost:8080/external-service-adapter/v1/process-sequence/v2 \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -d '{
    "applicationId": "APP123456",
    "customerId": "CUST123456",
    "workflowId": "123e4567-e89b-12d3-a456-426614174000",
    "partnerName": "SamplePartner",
    "programType": "PersonalLoan",
    "sequenceId": "SEQ123456",
    "sequenceString": "{1,2;5}"
  }'
```

## Example Response

```json
{
  "sequenceArray": [[1, 2], [5]],
  "data": {
    "request": {
      "applicationId": "APP123456",
      "customerId": "CUST123456",
      "workflowId": "123e4567-e89b-12d3-a456-426614174000",
      "partnerName": "SamplePartner",
      "programType": "PersonalLoan",
      "sequenceId": "SEQ123456"
    },
    "objectsWithFields": {
      "Lead": ["Id", "FirstName", "LastName"],
      "Contact": ["Id", "Email"]
    }
  },
  "services": [
    {
      "id": 1,
      "service_name": "LeadService",
      "api_url": "https://api.example.com/leads",
      "request_method": "GET",
      "request_body": {},
      "headers": {
        "Content-Type": "application/json"
      },
      "send_response": true,
      "responseBody": {},
      "time": 0,
      "status": "PENDING",
      "startTime": "",
      "endTime": ""
    },
    {
      "id": 2,
      "service_name": "ContactService",
      "api_url": "https://api.example.com/contacts",
      "request_method": "GET",
      "request_body": {},
      "headers": {
        "Content-Type": "application/json"
      },
      "send_response": true,
      "responseBody": {},
      "time": 0,
      "status": "PENDING",
      "startTime": "",
      "endTime": ""
    },
    {
      "id": 5,
      "service_name": "ValidationService",
      "api_url": "https://api.example.com/validate",
      "request_method": "POST",
      "request_body": {},
      "headers": {
        "Content-Type": "application/json"
      },
      "send_response": false,
      "responseBody": {},
      "time": 0,
      "status": "PENDING",
      "startTime": "",
      "endTime": ""
    }
  ]
}
```

This centralized structure makes it easier to manage service execution and maintain state throughout the entire workflow.

# Execution Model

The sequence executor follows a parallel-sequential execution pattern:

1. Each group in the `sequenceArray` is processed sequentially (one after another)
2. Within each group, services are executed in parallel
3. The system waits for all services in a group to complete before moving to the next group

This approach allows for:
- Executing independent services in parallel to improve performance
- Maintaining order when some services depend on the results of previous services
- Controlled execution flow with proper error handling

Example of execution flow with sequence array `[[1,2], [3,4], [5]]`:
- Services 1 and 2 are executed in parallel
- After both 1 and 2 complete, services 3 and 4 are executed in parallel
- After both 3 and 4 complete, service 5 is executed

#For Swagger Refresh, use following command to include all updates in APIs to the swagger generated document:
- swag init -g docs/swagger.go







## Note
Portfolio copy. Secrets, hostnames and credentials were replaced with placeholders (`REPLACE_ME_*`). The service depends on an internal shared Go module (`common-modules`) that is not included, so it will not build standalone.
