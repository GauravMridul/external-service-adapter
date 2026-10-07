package utility

import (
	"context"
	"esa/internal/app/constants"

	"github.com/gin-gonic/gin"
)

// AddIPHeadersToContext returns request context
func AddIPHeadersToContext(c *gin.Context, requestCtx context.Context) context.Context {
	forwardedForIPHeader := c.ClientIP()
	requestCtx = context.WithValue(requestCtx, constants.ForwardedForHeaderKey, forwardedForIPHeader)
	return requestCtx
}

func ContextForwardedIP(ctx context.Context) string {
	if ctxForwardedIP, ok := ctx.Value(constants.ForwardedForHeaderKey).(string); ok {
		return ctxForwardedIP
	}
	return ""
}
