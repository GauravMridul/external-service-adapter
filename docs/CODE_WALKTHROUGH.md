## External Service Adapter (ESA) – Code Walkthrough and Ownership Guide

### 1) Purpose and scope
- **What it is**: A Go microservice that orchestrates calls to external vendor APIs and executes configurable sequences of services with data-dependent control flow.
- **Key capabilities**:
  - Executes a sequence defined as groups with parallel-within-group and sequential-across-groups semantics.
  - Rich templating and expression engine to build requests and drive control flow (pre/post execution decisions).
  - Caching of service configuration and Salesforce query metadata.
  - Optional token management, client TLS, and S3 response archiving per service via `additional_config`.
  - Structured execution logs persisted to MongoDB.

### 2) High-level architecture
- **Entry**: `cmd/main.go` bootstraps common modules (config, logger, cache, api client), databases (Postgres + optional Mongo), auth middleware, and HTTP server (Gin).
- **API**: `internal/app/api/router/router.go` wires middleware and routes to controllers.
- **Controller**: `SequenceController.ProcessSequenceV2` validates request, enforces API key, and invokes the sequence service.
- **Core orchestration**: `internal/app/service/sequence_service/sequence_service.go`
  - Builds a `MasterDTO` holding sequence, input data, and per-service execution logs (`EsaLog`).
  - Fetches service configs (cached) and Salesforce data required by configs.
  - Executes each group in order; services inside a group run concurrently.
- **Invocation**: `ServiceInvoker` builds requests using the expression engine, handles token/certificates/S3, executes HTTP with retry and error mapping.
- **Templating/Expressions**: `ExpressionProcessor` resolves placeholders from DB data and prior service responses, supports transformations, arrays, JSON, numeric/boolean coercion, and custom functions.
- **Persistence**:
  - Configurations, query metadata: Postgres (GORM + Goose migrations).
  - Execution logs: MongoDB (`esa-log` collection).
- **Security**: API-key validation at controller, JWT/JWKS middleware in prod (bypass in dev).

### 3) Entrypoint and lifecycle
- `cmd/main.go`
  - Initializes common modules: `commoninit.InitializeCommonModules(commoninit.DefaultCommonModulesConfig())`.
  - Initializes DBs: `db.Init()` (SQL + migrations) and `db.InitMongoDB()` (optional), sets up repositories.
  - Initializes auth: `middleware/auth.Init()` → JWT middleware is enforced only in production.
  - Router: `router.NewRouter()` sets headers/CORS, health, swagger (non-prod), groups routes.
  - Server start: `http.Server.ListenAndServe()` with graceful shutdown on SIGINT/SIGTERM.
  - Global panic guards and emergency shutdown ensure clean teardown.

### 4) HTTP API surface
- Base path prefix: `constants.ExternalServiceAdapter = "/external-service-adapter"`.
- Health: `GET /external-service-adapter/health` (returns deployed `VERSION`).
- Swagger (non-prod): `GET /external-service-adapter/swagger/*any` proxied to generated doc JSON.
- Sequence execution:
  - **POST** `/external-service-adapter/v1/process-sequence/v2`
  - Security:
    - JWT middleware active in prod.
    - API key header `X-Api-Key` validated in the controller.
  - Request DTO `ProcessSequenceRequest`:
    - Required: `applicationId`, `customerId`, `workflowId` (uuid), `partnerName`, `programType`, `sequenceId`, `sequenceString`, `stage`.
  - Response: `MasterDTO` with two main sections:
    - `data`: database/Salesforce data snapshot used for templating.
    - `esaServices`: array of per-service `EsaLog` summaries (filtered fields, see Models section).

See example curl and sample response in `README.md`.

### 5) Controller details
- `internal/app/controller/sequence_controller.go`
  - Injects correlation/context, validates `X-Api-Key`, binds request body.
  - Calls `SequenceService.ProcessSequenceWithMasterDTO(ctx, &request)`.
  - On error, returns `500` with detailed `failed_services` info (status, code, error details).

### 6) Sequence orchestration
- `internal/app/service/sequence_service/sequence_service.go`
- Flow summary:
  1. Parse `sequenceString` into nested `[[...], [...]]` via `utility.ParseNestedSequenceString` → `MasterDTO.SequenceArray`.
  2. Collect service IDs, fetch service configurations using `CacheService.GetServiceConfigurations` (or repository fallback).
  3. Initialize `EsaLog` entries per service, hydrate request template (headers/body/URL), inject `additional_config` and static response if present.
  4. Extract SFDC objects/fields and conditions from configs; query Salesforce through `SalesforceClient`.
  5. Store normalized data in `MasterDTO.Data`.
  6. Execute groups sequentially; within a group services run in parallel using goroutines.
     - PreExecution/PostExecution decisions from `additional_config` expressions: `EXECUTE`, `CONTINUE` (skip), or `EXIT` (terminate sequence).
  7. Aggregate metrics (`overallStatus`, `TotalExecutionTime`), persist logs in Mongo via `EsaLogRepository.InsertLogs`.
- Error paths:
  - Pre/Post execution evaluation failures mark the service `FAILED` and can terminate the group/sequence depending on decision result.
  - Panics inside goroutines are recovered, recorded in the service response (`panic_details`) and surface to error channel.

### 7) Expressions and templating
- `internal/app/service/sequence_service/expression_processor.go`
- Placeholder types resolved in this order:
  - `{{ ... }}`: expressions (CALC, FORMAT, TRANSFORM, CONCAT, CONDITION, NUMERIC, BOOLEAN, JSON, ARRAY, nested support, and fallback with top-level `||`).
  - `<object.field>` and `<object[condition].field>`: values from `MasterDTO.Data` (object names case-insensitive; field lookup case-insensitive with nested array support).
  - `((ServiceName.field))`: values from prior/completed services’ responses.
- Type modes:
  - For request bodies, typed conversion is applied (numbers, booleans, arrays, JSON) so final HTTP payloads have correct JSON types.
- Selected custom functions: `generateId`, `calculateAge`, `validatePAN`, `formatPhone`, `getCurrentTimestamp`, `cleanSpecialChars`, `sha256Hash`, `dateWithinDays`, `daysSince`, `getNumericValue`, `calculateFOIR` (domain-specific FOIR math helpers included).

### 8) Service invocation
- `internal/app/service/sequence_service/service_invoker.go`
- Steps per service:
  1. Parse `additional_config` (supports `token_management`, `client_certificate`, `s3_response_upload`, plus `PreExecution`/`PostExecution` or `pre_execution`/`post_execution` — both key forms accepted; Cache/Retry).
  2. Generate a per-request correlation ID from `workflowId` + `sequenceId`.
  3. Prepare request:
     - Resolve placeholders in body/headers/URL via `ExpressionProcessor` (typed for body). Token placeholders `{{TOKEN_PLACEHOLDER}}` are preserved until token injection.
  4. If a pre-configured `response_body` is present (and `send_response` is false), short-circuit as success without HTTP.
  5. Token management (optional): obtain or refresh token via `TokenService`, inject into headers.
  6. Client certificate (optional): create HTTP client with TLS mutual-auth per config.
  7. Execute HTTP with timeout and optional retry on 401 (token refresh then retry).
  8. Parse/record response JSON or raw content, set `Status` = `COMPLETED` only for 2xx.
  9. If configured, upload response payload to S3 (optionally gzipped) and replace body with an S3 URL wrapper.
- Timeout resolution:
  - Per-service `timeout` in configuration (`additional_config.timeout`) takes precedence; otherwise global `RestExecuteTimeoutInSeconds` from config.

### 9) Caching layer
- `internal/app/service/cache_service/cache_service.go`
- **What we cache**:
  - Service configurations for a set of service IDs.
  - Salesforce query object relationship maps used to build composite SOQL queries.
- **Keys/TTL**:
  - Keys are deterministically generated; TTLs from `ESACacheConfig` (`ServiceConfigTTL`, `QueryObjectTTL`).
- **Fallbacks**: If Redis/cache is unavailable or errors, code falls back to repository and continues.

### 10) External integrations
- Salesforce:
  - `pkg/client/salesforce_client.go` performs Composite API calls; access token is acquired via username-password OAuth using configured credentials.
  - Supports token refresh on 401 and reattempt.
  - Builds SOQL based on requested fields and query relationships; supports additional conditions that reference fields from other objects using `@{...}` composite references.
- S3: Upload large responses via `s3_service` with dynamic credentials and metadata.
- Token providers: generic token issuance endpoints via `token_service` with cache-backed storage.

### 11) Data and persistence
- SQL (Postgres via GORM):
  - Table `service_configuration` with fields: `service_name`, `api_url`, `headers`, `request_body`, `request_method`, `response_body`, `send_response`, `timeout`, `additional_config` etc. Models in `esa_models/service_configuration.go`.
  - Migrations: `internal/app/db/migrations` (GORM and Goose helpers).
- MongoDB:
  - `esa-log` collection holds `EsaLog` documents with request/response snapshots, timing, statuses, metadata.
  - `models.EsaLog` implements custom `MarshalJSON` to hide request and some internal fields in HTTP responses while persisting full details in Mongo.

### 12) Middleware and security
- JWT/JWKS:
  - `middleware/auth` provides `AuthManager.AuthMiddleware()`. In production, validates `Authorization: Bearer ...` against JWKS and checks audience/issuer; in non-prod it bypasses.
- API Key:
  - `X-Api-Key` validated in controller against config value.
- Correlation ID:
  - Router middleware injects/propagates `X-Correlation-ID` header per request.
- CORS: Allowed origins read from config (`AllowedOrigins`).

### 13) Configuration
- Primary dev config: `configs/config.json` (K8s volume/file watcher supported in `common_init`).
- Important keys:
  - `service.name`, `Environment`, `server.port`, `log.level`.
  - Postgres: `database.*` (dialect, host, port, name, username, password, migration toggles).
  - Mongo: `mongo.connectionString`, `mongo.dbName`, `mongo.collections.esaLog`.
  - Cache: `cache.*` (host, port, db, timeouts, enabled, TTLs).
  - Salesforce: `salesforce.*` (baseurl, loginurl, username, password, securitytoken, clientid, clientsecret).
  - Auth (prod): `JwksAudience`, `JwksIssuer`, `JwksUrl`.
  - `X-Api-Key`: required by controller.
- Production notes:
  - Move secrets (db creds, Salesforce creds, API keys) to a secrets manager. The `common_init` supports AWS Secrets resolution via `config.VolumeConfigWatcher` options.

### 14) Build, run, local development
- Go: 1.23
- Local run (with Postgres/Redis/Mongo via compose):
  - `docker-compose up --build`
    - Exposes app on `:8080`.
    - Optional tools: Redis Commander (`:8081`), PgAdmin (`:8082`), Mongo Express (`:8083`).
- Binary build:
  - `go build -o main ./cmd/main.go`
- Docker:
  - Multi-stage `Dockerfile` builds binary, copies migrations and `VERSION`.
  - Uses BuildKit SSH mounts to fetch private `common-modules` from GitHub.

### 15) Deployment and CI/CD
- Containerized runtime from `Dockerfile`.
- Jenkins files present but empty in repo (`Jenkinsfile`, `Jenkinsfile-PR`). Typical pipeline stages you’ll want:
  - Checkout, `go mod download`, lint/test, build, image build/push, deploy to environment.
- Config reloading supported by volume watcher in `common_init`.

### 16) Observability and error handling
- Logging: Structured Zap logger via `common-modules` with context binding; extensive `Info/Infow/Warnw/Errorw` in critical paths.
- Correlation: `X-Correlation-ID` added by router; included in S3 metadata when enabled.
- Health check: `/external-service-adapter/health` returns deployed version.
- Panic recovery: Global guards in `main.go` and per-service goroutine recovery with detailed error attachment.

### 17) Ownership transfer checklist
- Config and secrets
  - Validate all `configs/*` are present per env; remove hardcoded secrets from VCS; ensure AWS Secret resolution enabled where required.
  - Set `X-Api-Key`, JWT `Jwks*` values for prod.
  - Set Salesforce credentials and endpoints for target org.
- Infrastructure
  - Postgres database provisioned; run migrations; seed `service_configuration` rows.
  - Redis available if caching is desired; or disable cache in config.
  - MongoDB for logs (optional but recommended); ensure network and auth.
- External dependencies
  - S3 bucket/keys/region if S3 uploads are used via `additional_config`.
  - Token endpoints and scopes for partners if `token_management` used.
  - Client certificate files/paths or secret names if `client_certificate` used.
- Build/Deploy
  - CI pipeline to build Docker image and deploy; BuildKit with SSH for private deps.
  - Runtime config mount/secret wiring in the environment.
- Documentation & Runbooks
  - Keep Swagger updated (`swag init -g docs/swagger.go`).
  - Document service IDs, their configs, and any partner-specific `additional_config` conventions.
  - SLOs, dashboards, alerting (if any) and log retention policy for `esa-log`.

### 18) How to add/update an external service
1. Insert/Update a row in `service_configuration`:
   - Fields include: `service_name`, `api_url`, `request_method`, `headers` (JSON), `request_body` (JSON), `send_response` (bool), `response_body` (JSON for pre-configured responses), `timeout`, `additional_config`.
2. Use placeholders to build dynamic requests:
   - From DB data: `<object.field>` or `<object[Condition].field>`.
   - From prior service responses: `((ServiceName.field))`.
   - Expressions: `{{FORMAT:...}}`, `{{TRANSFORM:...}}`, `{{JSON:...}}`, `{{ARRAY:...}}`, `{{NUMERIC:...}}`, etc.
3. Optional behaviors via `additional_config`:
   - `token_management`: endpoint, method, headers, body, cache key, `retry_on_401`, retries, `check_expiry_on_response`.
   - `client_certificate`: supply certificate type and materials; set TLS min/max.
   - `s3_response_upload`: enable threshold-based uploads.
   - `PreExecution` / `PostExecution` (or `pre_execution` / `post_execution` — both accepted): expressions returning `EXECUTE`/`CONTINUE`/`EXIT` to skip or terminate.

### 19) Troubleshooting guide
- Startup fails: “database ... is empty/not set” → verify `database.*` config and secrets resolution.
- Mongo connection fails → ensure `mongo.connectionString` and `dbName` are correct; Mongo reachable.
- No service configs found → verify `service_configuration` data and that SQL migrated; check cache fallback.
- Salesforce 401s → check creds, base/login URLs; token acquisition logs in `SalesforceClient`.
- Sequence exits early → inspect `PreExecution`/`PostExecution` expressions and resolved values in logs.
- Token management not injecting → ensure header includes `{{TOKEN_PLACEHOLDER}}` and token config is enabled.
- S3 upload missing → ensure `enabled`, bucket/region/keys, and response size exceeds `upload_threshold`.

### 20) Quickstart API example
Request:
```bash
curl -X POST http://localhost:8080/external-service-adapter/v1/process-sequence/v2 \
  -H "Content-Type: application/json" \
  -H "X-Api-Key: <your-api-key>" \
  -d '{
    "applicationId": "APP123456",
    "customerId": "CUST123456",
    "workflowId": "123e4567-e89b-12d3-a456-426614174000",
    "partnerName": "SamplePartner",
    "programType": "PersonalLoan",
    "sequenceId": "SEQ123456",
    "sequenceString": "{1,2;5}",
    "stage": "Decision"
  }'
```

Response (structure excerpt):
```json
{
  "data": { "Lead": { "Id": "..." }, "Contact": { /* ... */ } },
  "esaServices": [
    {
      "serviceId": 1,
      "serviceName": "LeadService",
      "status": "COMPLETED",
      "response": { "statusCode": 200, "body": { /* ... */ } }
    }
  ]
}
```

### 21) Key files map
- Entrypoint/server: `cmd/main.go`, `internal/app/api/router/router.go`, `internal/app/api/server/server.go`
- Controllers: `internal/app/controller/sequence_controller.go`, `health_controller.go`
- Services: `internal/app/service/sequence_service/*`, `cache_service/*`, `token_service/*`, `s3_service/*`, `certificate_service/*`
- Client: `pkg/client/salesforce_client.go`
- Models/DTOs: `internal/app/models/*`, `internal/app/dto/*`
- DB: `internal/app/db/*` (abstraction, migrations, repositories)
- Middleware: `middleware/auth/auth.go`
- Config: `configs/config.json` (dev), prod via volume/secret manager
- Swagger: `docs/*`

### 22) Notes for new owners
- The sequence engine is data-driven; most behavior is controlled by `service_configuration` records and `additional_config` JSON.
- Prefer using the cache service for frequently-read configs and query metadata; it gracefully degrades when Redis is unavailable.
- Keep expressions simple where possible; test complex expressions carefully in lower environments.
- Ensure secrets are not committed in config files for non-dev environments.

