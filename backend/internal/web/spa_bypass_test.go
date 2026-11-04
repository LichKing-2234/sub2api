package web

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The bypass list is shared by the embedded frontend middleware and is the only
// thing keeping backend API paths away from the SPA fallback. It lives outside
// the `embed` build-tag file so it can be tested without building the frontend.
func TestSPABypassKiroNativeProxyRoutes(t *testing.T) {
	for _, path := range []string{
		"/kiro",
		"/kiro/",
		"/kiro/generateAssistantResponse",
		"/kiro/GetProfile",
	} {
		require.True(t, shouldBypassEmbeddedFrontend(path), "path=%s", path)
	}

	for _, path := range []string{
		"/kirofoo",
		"/kiro-native-page",
		"/dashboard",
		"/",
	} {
		require.False(t, shouldBypassEmbeddedFrontend(path), "path=%s", path)
	}
}

func TestSPABypassExistingAPIPaths(t *testing.T) {
	for _, path := range []string{
		"/api/v1/admin/users",
		"/v1/messages",
		"/v1/responses/compact",
		"/v1beta/models",
		"/backend-api/codex/responses",
		"/antigravity/v1/messages",
		"/health",
		"/videos/generations",
	} {
		require.True(t, shouldBypassEmbeddedFrontend(path), "path=%s", path)
	}
}
