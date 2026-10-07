-- +goose Up
-- SQL in this section is executed when the migration is applied

CREATE TABLE IF NOT EXISTS service_configuration (
    id BIGSERIAL PRIMARY KEY,
    service_name VARCHAR(255) NOT NULL,
    api_url VARCHAR(512) NOT NULL,
    headers JSONB DEFAULT '{}',
    request_body JSONB DEFAULT '{}',
    request_method VARCHAR(255) NOT NULL,
    response_body JSONB DEFAULT '{}',
    send_response BOOLEAN DEFAULT FALSE,
    timeout INT4 DEFAULT 0,
    additional_config JSONB DEFAULT '{}',
    created_date TIMESTAMP NOT NULL,
    created_by VARCHAR(255) NOT NULL,
    modified_date TIMESTAMP,
    modified_by VARCHAR(255),
    is_deleted BOOLEAN DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS query_object_relationship_map (
    id BIGSERIAL PRIMARY KEY,
    query_object VARCHAR(255) NOT NULL,
    query_relation VARCHAR(255) NOT NULL,
    additional_fields TEXT,
    additional_conditions TEXT,
    created_date TIMESTAMP NOT NULL,
    created_by VARCHAR(255) NOT NULL,
    modified_date TIMESTAMP,
    modified_by VARCHAR(255),
    is_deleted BOOLEAN DEFAULT FALSE
);

-- +goose Down
-- SQL in this section is executed when the migration is rolled back

DROP TABLE IF EXISTS service_configuration;

DROP TABLE IF EXISTS query_object_relationship_map;