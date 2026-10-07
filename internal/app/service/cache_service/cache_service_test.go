package cache_service

import (
	"context"
	"errors"
	"testing"
	"time"

	"esa/internal/app/db/repository"
	"esa/internal/app/dto/common_dto"
	commoninit "esa/internal/app/init"
	"esa/internal/app/models/esa_models"

	"github.com/dmi-infotech/common-modules/go/contracts"
)

type testLogger struct{}

func (l *testLogger) Debug(msg string)                                 {}
func (l *testLogger) Debugw(msg string, keysAndValues ...interface{})  {}
func (l *testLogger) Debugf(format string, args ...interface{})        {}
func (l *testLogger) Info(args ...interface{})                         {}
func (l *testLogger) Infow(msg string, keysAndValues ...interface{})   {}
func (l *testLogger) Infof(format string, args ...interface{})         {}
func (l *testLogger) Warn(msg string)                                  {}
func (l *testLogger) Warnw(msg string, keysAndValues ...interface{})   {}
func (l *testLogger) Warnf(format string, args ...interface{})         {}
func (l *testLogger) Error(msg string)                                 {}
func (l *testLogger) Errorw(msg string, keysAndValues ...interface{})  {}
func (l *testLogger) Errorf(format string, args ...interface{})        {}
func (l *testLogger) Fatal(msg string)                                 {}
func (l *testLogger) Fatalf(format string, args ...interface{})        {}
func (l *testLogger) WithContext(ctx context.Context) contracts.Logger { return l }
func (l *testLogger) WithFields(fields map[string]interface{}) contracts.Logger {
	return l
}

type fakeCache struct {
	commoninit.NoOpCacheProvider
	getValue   string
	getErr     error
	setCalls   int
	lastSetKey string
	lastSetTTL time.Duration
	lastSetRaw interface{}
}

func (c *fakeCache) Get(ctx context.Context, key string) (string, error) {
	return c.getValue, c.getErr
}

func (c *fakeCache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	c.setCalls++
	c.lastSetKey = key
	c.lastSetTTL = ttl
	c.lastSetRaw = value
	return nil
}

type fakeServiceConfigRepo struct {
	response []*esa_models.ServiceConfigurationResponse
	err      error
	calls    int
}

func (r *fakeServiceConfigRepo) FindByServiceIdArray(ctx context.Context, serviceIds []int) ([]*esa_models.ServiceConfigurationResponse, error) {
	r.calls++
	return r.response, r.err
}

type fakeQueryObjectRepo struct {
	response      map[string]*esa_models.QueryObjectRelationshipMap
	err           error
	calls         int
	labelResponse map[string]*esa_models.QueryObjectRelationshipMap
	labelErr      error
	labelCalls    int
}

func (r *fakeQueryObjectRepo) FindByObjectName(ctx context.Context, objectName string) (*esa_models.QueryObjectRelationshipMap, error) {
	if v, ok := r.response[objectName]; ok {
		return v, nil
	}
	return nil, errors.New("not found")
}

func (r *fakeQueryObjectRepo) FindByObjectNames(ctx context.Context, objectNames []string) (map[string]*esa_models.QueryObjectRelationshipMap, error) {
	r.calls++
	return r.response, r.err
}

func (r *fakeQueryObjectRepo) FindByLabels(ctx context.Context, labels []string) (map[string]*esa_models.QueryObjectRelationshipMap, error) {
	r.labelCalls++
	return r.labelResponse, r.labelErr
}

var _ repository.IServiceConfigurationRepository = (*fakeServiceConfigRepo)(nil)
var _ repository.IQueryObjectRepository = (*fakeQueryObjectRepo)(nil)

func TestGetServiceConfigurations_SetsCacheAfterDBFetch(t *testing.T) {
	t.Parallel()

	mockCache := &fakeCache{getValue: "", getErr: nil}
	mockServiceRepo := &fakeServiceConfigRepo{
		response: []*esa_models.ServiceConfigurationResponse{
			{ID: 120, ServiceName: "Actico_kyc"},
			{ID: 90, ServiceName: "IncomeModel"},
		},
	}
	mockQueryRepo := &fakeQueryObjectRepo{}

	svc := &CacheService{
		cache:           mockCache,
		keyBuilder:      newCacheKeyBuilder(),
		cacheConfig:     &common_dto.ESACacheConfig{ServiceConfigTTL: 3 * time.Minute},
		logger:          &testLogger{},
		serviceRepo:     mockServiceRepo,
		queryObjectRepo: mockQueryRepo,
	}

	serviceIDs := []int{120, 90}
	result, err := svc.GetServiceConfigurations(context.Background(), serviceIDs)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(result))
	}
	if mockServiceRepo.calls != 1 {
		t.Fatalf("expected DB repo to be called once, got %d", mockServiceRepo.calls)
	}
	if mockCache.setCalls != 1 {
		t.Fatalf("expected cache set to be called once, got %d", mockCache.setCalls)
	}

	expectedKey := newCacheKeyBuilder().ServiceConfigKey(serviceIDs)
	if mockCache.lastSetKey != expectedKey {
		t.Fatalf("expected cache key %q, got %q", expectedKey, mockCache.lastSetKey)
	}
	if mockCache.lastSetTTL != 3*time.Minute {
		t.Fatalf("expected TTL %v, got %v", 3*time.Minute, mockCache.lastSetTTL)
	}
}

func TestGetQueryObjectRelationships_SetsCacheAfterDBFetch(t *testing.T) {
	t.Parallel()

	mockCache := &fakeCache{getValue: "", getErr: nil}
	mockServiceRepo := &fakeServiceConfigRepo{}
	mockQueryRepo := &fakeQueryObjectRepo{
		response: map[string]*esa_models.QueryObjectRelationshipMap{
			"lead": {
				ID:            1,
				QueryObject:   "lead",
				QueryRelation: "id = '{{lead.id}}'",
			},
			"contact": {
				ID:            2,
				QueryObject:   "contact",
				QueryRelation: "id = '{{contact.id}}'",
			},
		},
	}

	svc := &CacheService{
		cache:           mockCache,
		keyBuilder:      newCacheKeyBuilder(),
		cacheConfig:     &common_dto.ESACacheConfig{QueryObjectTTL: 7 * time.Minute},
		logger:          &testLogger{},
		serviceRepo:     mockServiceRepo,
		queryObjectRepo: mockQueryRepo,
	}

	objectNames := []string{"lead", "contact"}
	result, err := svc.GetQueryObjectRelationships(context.Background(), objectNames)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 query-object relationships, got %d", len(result))
	}
	if mockQueryRepo.calls != 1 {
		t.Fatalf("expected DB repo to be called once, got %d", mockQueryRepo.calls)
	}
	if mockCache.setCalls != 1 {
		t.Fatalf("expected cache set to be called once, got %d", mockCache.setCalls)
	}

	expectedKey := newCacheKeyBuilder().QueryObjectKey(objectNames)
	if mockCache.lastSetKey != expectedKey {
		t.Fatalf("expected cache key %q, got %q", expectedKey, mockCache.lastSetKey)
	}
	if mockCache.lastSetTTL != 7*time.Minute {
		t.Fatalf("expected TTL %v, got %v", 7*time.Minute, mockCache.lastSetTTL)
	}
}

func TestGetServiceConfigurations_SetsCacheWhenGetReturnsError(t *testing.T) {
	t.Parallel()

	mockCache := &fakeCache{getErr: errors.New("key not found")}
	mockServiceRepo := &fakeServiceConfigRepo{
		response: []*esa_models.ServiceConfigurationResponse{
			{ID: 120, ServiceName: "Actico_kyc"},
		},
	}
	mockQueryRepo := &fakeQueryObjectRepo{}

	svc := &CacheService{
		cache:           mockCache,
		keyBuilder:      newCacheKeyBuilder(),
		cacheConfig:     &common_dto.ESACacheConfig{ServiceConfigTTL: 3 * time.Minute},
		logger:          &testLogger{},
		serviceRepo:     mockServiceRepo,
		queryObjectRepo: mockQueryRepo,
	}

	_, err := svc.GetServiceConfigurations(context.Background(), []int{120})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if mockCache.setCalls != 1 {
		t.Fatalf("expected cache set to be called on cache get error, got %d", mockCache.setCalls)
	}
}
