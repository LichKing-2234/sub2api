package web

import "strings"

// shouldBypassEmbeddedFrontend reports whether an HTTP path belongs to a
// backend API surface and must NOT be handled by the embedded SPA fallback.
//
// It lives outside the `embed` build-tag file so the bypass list can be unit
// tested without building the Vue frontend assets.
func shouldBypassEmbeddedFrontend(path string) bool {
	trimmed := strings.TrimSpace(path)
	return strings.HasPrefix(trimmed, "/api/") ||
		strings.HasPrefix(trimmed, "/v1/") ||
		strings.HasPrefix(trimmed, "/v1beta/") ||
		strings.HasPrefix(trimmed, "/backend-api/") ||
		strings.HasPrefix(trimmed, "/antigravity/") ||
		strings.HasPrefix(trimmed, "/setup/") ||
		// Kiro native thin proxy: the Kiro CLI/IDE endpoint override points at
		// /kiro and posts the native protocol there. Every suffix must reach the
		// gin route instead of the SPA fallback.
		trimmed == "/kiro" ||
		strings.HasPrefix(trimmed, "/kiro/") ||
		trimmed == "/health" ||
		trimmed == "/models" ||
		strings.HasPrefix(trimmed, "/models/") ||
		trimmed == "/responses" ||
		strings.HasPrefix(trimmed, "/responses/") ||
		trimmed == "/chat/completions" ||
		trimmed == "/embeddings" ||
		trimmed == "/messages/count_tokens" ||
		trimmed == "/alpha/search" ||
		trimmed == "/web_search" ||
		trimmed == "/x_search" ||
		trimmed == "/tts" ||
		trimmed == "/stt" ||
		trimmed == "/realtime" ||
		trimmed == "/custom-voices" ||
		strings.HasPrefix(trimmed, "/custom-voices/") ||
		trimmed == "/contents/generations/tasks" ||
		strings.HasPrefix(trimmed, "/contents/generations/tasks/") ||
		trimmed == "/v3/contents/generations/tasks" ||
		strings.HasPrefix(trimmed, "/v3/contents/generations/tasks/") ||
		strings.HasPrefix(trimmed, "/images/") ||
		trimmed == "/videos" ||
		strings.HasPrefix(trimmed, "/videos/")
}
