package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// kiroNativeRoutePrefix is the client-facing entry point for the Kiro native
// protocol thin proxy. Kiro CLI/IDE endpoint overrides point here (e.g.
// https://sub2api.example.com/kiro); the handler forwards the raw native request
// to the kiro2cc native inbound.
const kiroNativeRoutePrefix = "/kiro"

// RegisterKiroNativeRoutes registers the additive Kiro native thin proxy route.
//
// The route reuses the regular API-key auth middleware, so client validation,
// group assignment, balance/subscription/quota enforcement and context
// population behave exactly like the other gateway routes. The handler itself is
// a raw reverse proxy that records usage parsed from native EventStream frames.
func RegisterKiroNativeRoutes(
	r *gin.Engine,
	h *handler.Handlers,
	apiKeyAuth middleware.APIKeyAuthMiddleware,
	opsService *service.OpsService,
	cfg *config.Config,
) {
	if r == nil || h == nil || h.Gateway == nil {
		return
	}

	var maxBody int64 = 16 << 20
	if cfg != nil {
		maxBody = cfg.KiroNative.EffectiveMaxBodyBytes()
	}

	group := r.Group(kiroNativeRoutePrefix)
	group.Use(middleware.RequestBodyLimit(maxBody))
	group.Use(middleware.ClientRequestID())
	group.Use(handler.OpsErrorLoggerMiddleware(opsService))
	group.Use(handler.InboundEndpointMiddleware())
	group.Use(gin.HandlerFunc(apiKeyAuth))
	group.POST("", h.Gateway.KiroNative)
	// The Kiro client always posts to its configured endpoint root (with
	// optional query string); "/" and deeper suffixes are both matched here.
	group.POST("/*path", h.Gateway.KiroNative)
}
