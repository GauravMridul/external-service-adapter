package utility

import (
	"context"
	"time"

	"github.com/dmi-infotech/common-modules/go/contracts"
	cmcontext "github.com/dmi-infotech/common-modules/go/utils/context"
)

// Context keys for request-scoped values (use unexported type to avoid collisions)
type esaContextKey struct{}

var (
	requestStartTimeKey esaContextKey
	requestLoggerKey    esaContextKey
	groupIndexKey       esaContextKey
)

// SetRequestStartTime stores the request start time in context
func SetRequestStartTime(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, requestStartTimeKey, t)
}

// GetRequestStartTime returns the request start time from context, or zero value if not set
func GetRequestStartTime(ctx context.Context) time.Time {
	if t, ok := ctx.Value(requestStartTimeKey).(time.Time); ok {
		return t
	}
	return time.Time{}
}

// SetRequestLogger stores the request-scoped logger (with correlation_id, lead_id=customerId, stage) in context
func SetRequestLogger(ctx context.Context, logger contracts.Logger) context.Context {
	ctx = context.WithValue(ctx, requestLoggerKey, logger)
	return cmcontext.AddRequestLoggerToContext(ctx, logger)
}

// GetRequestLogger returns the request-scoped logger from context, or nil if not set
func GetRequestLogger(ctx context.Context) contracts.Logger {
	if l := cmcontext.GetRequestLoggerFromContext(ctx); l != nil {
		return l
	}
	if l, ok := ctx.Value(requestLoggerKey).(contracts.Logger); ok {
		return l
	}
	return nil
}

// SetGroupIndex stores the current group index (0-based) for ESA sequence execution
func SetGroupIndex(ctx context.Context, groupIndex int) context.Context {
	return context.WithValue(ctx, groupIndexKey, groupIndex)
}

// GetGroupIndex returns the group index from context, or -1 if not set
func GetGroupIndex(ctx context.Context) int {
	if i, ok := ctx.Value(groupIndexKey).(int); ok {
		return i
	}
	return -1
}
