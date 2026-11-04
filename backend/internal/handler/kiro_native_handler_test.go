//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiroeventstream"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubKiroNativeBilling struct {
	account    *service.Account
	accountErr error
	recordErr  error
	recorded   []*service.RecordUsageInput
}

func (s *stubKiroNativeBilling) AttributionAccount(_ context.Context, _ *int64) (*service.Account, error) {
	return s.account, s.accountErr
}

func (s *stubKiroNativeBilling) RecordUsage(_ context.Context, input *service.RecordUsageInput) error {
	s.recorded = append(s.recorded, input)
	return s.recordErr
}

func newKiroNativeTestHandler(t *testing.T, cfg *config.Config, billing kiroNativeBilling) *GatewayHandler {
	t.Helper()
	oldFactory := newKiroNativeBilling
	newKiroNativeBilling = func(*GatewayHandler) kiroNativeBilling { return billing }
	t.Cleanup(func() { newKiroNativeBilling = oldFactory })
	return &GatewayHandler{cfg: cfg}
}

func newKiroNativeTestAPIKey() *service.APIKey {
	groupID := int64(7)
	return &service.APIKey{
		ID:      11,
		UserID:  12,
		GroupID: &groupID,
		Status:  service.StatusActive,
		Group: &service.Group{
			ID:       groupID,
			Platform: service.PlatformAnthropic,
			Status:   service.StatusActive,
		},
		User: &service.User{ID: 12, Concurrency: 5, Balance: 100},
	}
}

func newKiroNativeTestContext(requestBody string, path string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(requestBody))
	c.Request.Header.Set("Content-Type", "application/x-amz-json-1.0")
	c.Request.Header.Set("x-amz-target", "AmazonCodeWhispererStreamingService.GenerateAssistantResponse")
	c.Request.Header.Set("Authorization", "Bearer sk-user-key")
	c.Request.Header.Set("X-Api-Key", "sk-user-key")
	c.Request.Header.Set("User-Agent", "aws-sdk-rust/1.6.0 app/AmazonQ-For-CLI")
	c.Set(string(middleware2.ContextKeyAPIKey), newKiroNativeTestAPIKey())
	return c, recorder
}

func kiroNativeNativeBody() string {
	return `{"conversationState":{"conversationId":"conv-1","history":[],"currentMessage":{"userInputMessage":{"content":"hi","modelId":"claude-sonnet-4.6"}}}}`
}

func kiroNativeUsageFrames() []byte {
	var data []byte
	data = append(data, kiroeventstream.EncodeFrame(map[string]string{
		":message-type": "event",
		":event-type":   "assistantResponseEvent",
	}, []byte(`{"content":"hi"}`))...)
	data = append(data, kiroeventstream.EncodeFrame(map[string]string{
		":message-type": "event",
		":event-type":   "metadataEvent",
	}, []byte(`{"stopReason":"end_turn","tokenUsage":{"uncachedInputTokens":11,"outputTokens":5,"totalTokens":16,"cacheReadInputTokens":2,"cacheWriteInputTokens":1}}`))...)
	data = append(data, kiroeventstream.EncodeFrame(map[string]string{
		":message-type": "event",
		":event-type":   "meteringEvent",
	}, []byte(`{"unit":"credit","unitPlural":"credits","usage":0.25}`))...)
	data = append(data, kiroeventstream.EncodeFrame(map[string]string{
		":message-type": "event",
		":event-type":   "contextUsageEvent",
	}, []byte(`{"contextUsagePercentage":12.5}`))...)
	return data
}

func TestKiroNativeProxiesRawRequestAndRecordsUsage(t *testing.T) {
	var gotPath, gotToken, gotAuthorization, gotAPIKeyHeader, gotTarget, gotContentType string
	var gotBody []byte
	frames := kiroNativeUsageFrames()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get(kiroNativeTokenHeader)
		gotAuthorization = r.Header.Get("Authorization")
		gotAPIKeyHeader = r.Header.Get("X-Api-Key")
		gotTarget = r.Header.Get("x-amz-target")
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.Header().Set("x-amzn-requestid", "req-native-1")
		w.Header().Set("Connection", "X-Should-Be-Dropped")
		w.Header().Set("X-Should-Be-Dropped", "1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(frames)
	}))
	defer upstream.Close()

	billing := &stubKiroNativeBilling{account: &service.Account{
		ID: 55, Name: "kiro2cc-upstream", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
	}}
	cfg := &config.Config{KiroNative: config.KiroNativeConfig{
		Upstream:     upstream.URL + "/native",
		Token:        "native-internal-token",
		MaxBodyBytes: 1 << 20,
	}}
	h := newKiroNativeTestHandler(t, cfg, billing)
	c, recorder := newKiroNativeTestContext(kiroNativeNativeBody(), "/kiro/")

	h.KiroNative(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "application/vnd.amazon.eventstream", recorder.Header().Get("Content-Type"))
	require.Equal(t, "req-native-1", recorder.Header().Get("x-amzn-requestid"))
	require.Empty(t, recorder.Header().Get("X-Should-Be-Dropped"), "hop-by-hop listed headers must not be copied")
	require.Equal(t, frames, recorder.Body.Bytes())

	// Upstream request: raw body and native headers preserved, client credentials
	// stripped, internal token injected.
	require.Equal(t, "/native", gotPath)
	require.Equal(t, kiroNativeNativeBody(), string(gotBody))
	require.Equal(t, "native-internal-token", gotToken)
	require.Empty(t, gotAuthorization)
	require.Empty(t, gotAPIKeyHeader)
	require.Equal(t, "AmazonCodeWhispererStreamingService.GenerateAssistantResponse", gotTarget)
	require.Equal(t, "application/x-amz-json-1.0", gotContentType)

	// Usage row recorded through the billing seam with tokens parsed from the
	// native EventStream frames.
	require.Len(t, billing.recorded, 1)
	input := billing.recorded[0]
	require.NotNil(t, input.Result)
	require.Equal(t, "claude-sonnet-4.6", input.Result.Model)
	require.True(t, input.Result.Stream)
	require.Equal(t, 11, input.Result.Usage.InputTokens)
	require.Equal(t, 5, input.Result.Usage.OutputTokens)
	require.Equal(t, 2, input.Result.Usage.CacheReadInputTokens)
	require.Equal(t, 1, input.Result.Usage.CacheCreationInputTokens)
	require.Equal(t, "req-native-1", input.Result.RequestID)
	require.Equal(t, int64(55), input.Account.ID)
	require.Equal(t, int64(11), input.APIKey.ID)
	require.Equal(t, int64(12), input.User.ID)
	require.Equal(t, "/kiro", input.InboundEndpoint)
	require.Equal(t, "/native", input.UpstreamEndpoint)
	require.Equal(t, "conv-1", input.SessionID)
	require.Equal(t, service.PlatformAnthropic, input.QuotaPlatform)
}

func TestKiroNativeDisabledUpstream(t *testing.T) {
	h := newKiroNativeTestHandler(t, &config.Config{}, &stubKiroNativeBilling{})
	c, recorder := newKiroNativeTestContext(`{}`, "/kiro")

	h.KiroNative(c)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestKiroNativeRequiresAPIKey(t *testing.T) {
	h := newKiroNativeTestHandler(t, &config.Config{KiroNative: config.KiroNativeConfig{Upstream: "http://127.0.0.1:1/native"}}, &stubKiroNativeBilling{})
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/kiro", strings.NewReader(`{}`))

	h.KiroNative(c)

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestKiroNativeRejectsOversizedBody(t *testing.T) {
	billing := &stubKiroNativeBilling{account: &service.Account{ID: 1}}
	cfg := &config.Config{KiroNative: config.KiroNativeConfig{
		Upstream:     "http://127.0.0.1:1/native",
		MaxBodyBytes: 16,
	}}
	h := newKiroNativeTestHandler(t, cfg, billing)
	c, recorder := newKiroNativeTestContext(strings.Repeat("x", 64), "/kiro")

	h.KiroNative(c)

	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	require.Empty(t, billing.recorded)
}

func TestKiroNativeAttributionUnavailableFailsClosed(t *testing.T) {
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	billing := &stubKiroNativeBilling{accountErr: errors.New("no account")}
	cfg := &config.Config{KiroNative: config.KiroNativeConfig{Upstream: upstream.URL + "/native"}}
	h := newKiroNativeTestHandler(t, cfg, billing)
	c, recorder := newKiroNativeTestContext(kiroNativeNativeBody(), "/kiro/")

	h.KiroNative(c)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.False(t, upstreamCalled, "inference must not be proxied without a usage attribution account")
}

func TestKiroNativePassesThroughUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"throttled"}`))
	}))
	defer upstream.Close()

	billing := &stubKiroNativeBilling{account: &service.Account{ID: 1}}
	cfg := &config.Config{KiroNative: config.KiroNativeConfig{Upstream: upstream.URL + "/native"}}
	h := newKiroNativeTestHandler(t, cfg, billing)
	c, recorder := newKiroNativeTestContext(kiroNativeNativeBody(), "/kiro/")

	h.KiroNative(c)

	require.Equal(t, http.StatusTooManyRequests, recorder.Code)
	require.Contains(t, recorder.Body.String(), "throttled")
	require.Empty(t, billing.recorded, "error responses must not record usage")
}

func TestKiroNativeControlPlaneJSONHasNoUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer upstream.Close()

	billing := &stubKiroNativeBilling{account: &service.Account{ID: 1}}
	cfg := &config.Config{KiroNative: config.KiroNativeConfig{Upstream: upstream.URL + "/native"}}
	h := newKiroNativeTestHandler(t, cfg, billing)
	c, recorder := newKiroNativeTestContext(`{"origin":"KIRO_CLI"}`, "/kiro/")
	c.Request.Header.Set("x-amz-target", "KiroControlPlaneBearerService.ListAvailableModels")

	h.KiroNative(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"models":[]}`, recorder.Body.String())
	require.Empty(t, billing.recorded)
}

func TestKiroNativeUpstreamTransportError(t *testing.T) {
	billing := &stubKiroNativeBilling{account: &service.Account{ID: 1}}
	cfg := &config.Config{KiroNative: config.KiroNativeConfig{Upstream: "http://127.0.0.1:1/native"}}
	h := newKiroNativeTestHandler(t, cfg, billing)
	c, recorder := newKiroNativeTestContext(kiroNativeNativeBody(), "/kiro/")

	h.KiroNative(c)

	require.Equal(t, http.StatusBadGateway, recorder.Code)
}

func TestKiroNativeModelExtraction(t *testing.T) {
	require.Equal(t, "claude-sonnet-4.6", kiroNativeRequestModel([]byte(kiroNativeNativeBody())))
	require.Equal(t, "claude-x", kiroNativeRequestModel([]byte(`{"modelId":"claude-x"}`)))
	require.Empty(t, kiroNativeRequestModel(nil))
	require.Empty(t, kiroNativeRequestModel([]byte(`{not json`)))
}

func TestKiroNativeUpstreamURLMapping(t *testing.T) {
	cases := []struct {
		base     string
		path     string
		query    string
		want     string
		wantFail bool
	}{
		{base: "http://upstream:5678/native", path: "/kiro", want: "http://upstream:5678/native"},
		{base: "http://upstream:5678/native/", path: "/kiro/", want: "http://upstream:5678/native"},
		{base: "http://upstream:5678/native", path: "/kiro/", want: "http://upstream:5678/native"},
		{base: "http://upstream:5678/native", path: "/kiro/extra/path", want: "http://upstream:5678/native/extra/path"},
		{base: "http://upstream:5678/native", path: "/kiro/", query: "origin=KIRO_CLI", want: "http://upstream:5678/native?origin=KIRO_CLI"},
		{base: "", path: "/kiro", wantFail: true},
		{base: "not-a-url", path: "/kiro", wantFail: true},
	}
	for _, tc := range cases {
		rawURL := tc.path
		if tc.query != "" {
			rawURL += "?" + tc.query
		}
		reqURL, err := url.Parse(rawURL)
		require.NoError(t, err)
		got, err := kiroNativeUpstreamURL(tc.base, reqURL)
		if tc.wantFail {
			require.Error(t, err, "case %+v", tc)
			continue
		}
		require.NoError(t, err, "case %+v", tc)
		require.Equal(t, tc.want, got, "case %+v", tc)
	}
}

func TestKiroNativeUsageFramesAreJSONSerializable(t *testing.T) {
	// Guard against accidental changes to the billing seam input shape.
	result := service.ForwardResult{
		Usage: service.ClaudeUsage{InputTokens: 1, OutputTokens: 2},
	}
	_, err := json.Marshal(result)
	require.NoError(t, err)
}
