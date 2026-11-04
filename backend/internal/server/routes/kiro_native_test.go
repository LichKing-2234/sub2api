//go:build unit

package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newKiroNativeRoutesTestRouter(cfg *config.Config) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	apiKeyAuth := func(c *gin.Context) { c.Next() }
	RegisterKiroNativeRoutes(router, &handler.Handlers{Gateway: &handler.GatewayHandler{}}, apiKeyAuth, nil, cfg)
	return router
}

func TestKiroNativeRouteRegistered(t *testing.T) {
	router := newKiroNativeRoutesTestRouter(&config.Config{})

	for _, path := range []string{"/kiro", "/kiro/", "/kiro/GetProfile", "/kiro/generateAssistantResponse"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		// Disabled upstream: the handler answers 503, proving the request reached
		// the native proxy instead of the gin 404 / SPA fallback.
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, "path=%s", path)
	}
}

func TestKiroNativeRouteRejectsOtherMethods(t *testing.T) {
	router := newKiroNativeRoutesTestRouter(&config.Config{})
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, "/kiro", nil))
		require.Equal(t, http.StatusNotFound, recorder.Code, "method=%s", method)
	}
}

func TestKiroNativeRouteBodyLimit(t *testing.T) {
	router := newKiroNativeRoutesTestRouter(&config.Config{
		KiroNative: config.KiroNativeConfig{MaxBodyBytes: 8},
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/kiro", strings.NewReader(strings.Repeat("x", 64))))
	// The handler is disabled in this config, so 503 is expected before the body
	// is read; this test mainly guarantees the route chain (limit middleware +
	// handler) stays registered and does not panic.
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}
