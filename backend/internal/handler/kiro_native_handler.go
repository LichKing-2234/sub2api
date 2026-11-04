package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiroeventstream"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const (
	// kiroNativeRoutePrefix is the client-facing prefix for the native Kiro
	// protocol; local Kiro CLI/IDE settings point their endpoint here, e.g.
	// https://sub2api.example.com/kiro.
	kiroNativeRoutePrefix = "/kiro"
	// kiroNativeTokenHeader authenticates sub2api to the kiro2cc native inbound.
	kiroNativeTokenHeader = "X-Kiro-Native-Token"
	kiroNativeComponent   = "handler.kiro_native"
	kiroNativeReadBuffer  = 32 * 1024
)

// kiroNativeBilling abstracts the usage attribution/billing dependencies of the
// native thin proxy. Production delegates to GatewayService; unit tests swap it
// for a stub through the newKiroNativeBilling package variable.
type kiroNativeBilling interface {
	AttributionAccount(ctx context.Context, groupID *int64) (*service.Account, error)
	RecordUsage(ctx context.Context, input *service.RecordUsageInput) error
}

type gatewayKiroNativeBilling struct {
	gateway         *service.GatewayService
	pinnedAccountID int64
}

func (b gatewayKiroNativeBilling) AttributionAccount(ctx context.Context, groupID *int64) (*service.Account, error) {
	return b.gateway.KiroNativeAttributionAccount(ctx, groupID, b.pinnedAccountID)
}

func (b gatewayKiroNativeBilling) RecordUsage(ctx context.Context, input *service.RecordUsageInput) error {
	return b.gateway.RecordUsage(ctx, input)
}

// newKiroNativeBilling is a seam over GatewayService for unit tests.
var newKiroNativeBilling = func(h *GatewayHandler) kiroNativeBilling {
	var pinned int64
	if h != nil && h.cfg != nil {
		pinned = h.cfg.KiroNative.AttributionAccountID
	}
	return gatewayKiroNativeBilling{gateway: h.gatewayService, pinnedAccountID: pinned}
}

// KiroNative proxies the Kiro native protocol to the kiro2cc native inbound.
//
// The route is a thin, raw reverse proxy: it authenticates the client with the
// regular API-key middleware, forwards the native request verbatim, streams the
// native response (AWS EventStream or JSON) back unbuffered, and parses usage
// frames to record usage_logs through the existing billing service. No protocol
// translation is performed.
func (h *GatewayHandler) KiroNative(c *gin.Context) {
	cfg := config.KiroNativeConfig{}
	if h != nil && h.cfg != nil {
		cfg = h.cfg.KiroNative
	}
	reqLog := requestLogger(c, kiroNativeComponent)
	if !cfg.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "kiro native proxy is not configured"})
		return
	}

	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "invalid api key"})
		return
	}
	user := apiKey.User
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "user of the api key not found"})
		return
	}

	maxBody := cfg.EffectiveMaxBodyBytes()
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxBody))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": fmt.Sprintf("request body too large (max %d bytes)", maxBody)})
			return
		}
		reqLog.Warn("kiro_native.read_body_failed", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"message": "failed to read request body"})
		return
	}

	model := kiroNativeRequestModel(body)
	if apiKey.Group != nil && model != "" && apiKey.Group.ModelAllowlistEnabled() && !apiKey.Group.ModelAllowlist.Allows(model) {
		// log-only for the POC: kiro2cc (upstream) is the authority for the
		// effective model, and this route must not silently reject native traffic
		// because of a stale allowlist while the feature is being validated.
		reqLog.Warn("kiro_native.model_not_in_group_allowlist",
			zap.Int64("group_id", derefInt64(apiKey.GroupID)),
			zap.String("model", model),
		)
	}

	targetURL, err := kiroNativeUpstreamURL(cfg.Upstream, c.Request.URL)
	if err != nil {
		reqLog.Error("kiro_native.build_upstream_url_failed", zap.Error(err))
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "kiro native upstream is misconfigured"})
		return
	}

	isInference := kiroNativeIsInferenceRequest(c, targetURL)
	var attributionAccount *service.Account
	billing := newKiroNativeBilling(h)
	if isInference {
		attributionAccount, err = billing.AttributionAccount(c.Request.Context(), apiKey.GroupID)
		if err != nil {
			reqLog.Error("kiro_native.attribution_account_unavailable", zap.Error(err),
				zap.Int64("group_id", derefInt64(apiKey.GroupID)))
			c.JSON(http.StatusServiceUnavailable, gin.H{"message": "kiro native usage attribution is unavailable"})
			return
		}
	}

	startTime := time.Now()
	upstreamCtx, cancelUpstream := context.WithTimeout(c.Request.Context(), cfg.EffectiveTimeout())
	defer cancelUpstream()

	upstreamReq, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		reqLog.Error("kiro_native.build_request_failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to build upstream request"})
		return
	}
	copyKiroNativeRequestHeaders(c.Request.Header, upstreamReq.Header)
	if token := strings.TrimSpace(cfg.Token); token != "" {
		upstreamReq.Header.Set(kiroNativeTokenHeader, token)
	}
	if upstreamReq.Header.Get("Content-Type") == "" {
		upstreamReq.Header.Set("Content-Type", "application/json")
	}

	reqLog.Info("kiro_native.forward",
		zap.String("upstream", targetURL),
		zap.String("target", upstreamReq.Header.Get("x-amz-target")),
		zap.Int("body_bytes", len(body)),
		zap.String("model", model),
	)

	client := &http.Client{Transport: http.DefaultTransport}
	resp, err := client.Do(upstreamReq)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(upstreamCtx.Err(), context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		reqLog.Warn("kiro_native.upstream_failed",
			zap.String("upstream", targetURL),
			zap.Int("status", status),
			zap.Error(err),
		)
		c.JSON(status, gin.H{"message": "kiro native upstream request failed"})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	copyKiroNativeResponseHeaders(resp.Header, c.Writer.Header())
	c.Writer.WriteHeader(resp.StatusCode)

	isEventStream := isKiroNativeEventStreamResponse(resp.Header, isInference)
	decoder := kiroeventstream.NewDecoder()
	var usage kiroeventstream.Usage
	var firstByteMs *int
	clientDisconnected := false
	written := 0

	buf := make([]byte, kiroNativeReadBuffer)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if firstByteMs == nil {
				ms := int(time.Since(startTime).Milliseconds())
				firstByteMs = &ms
			}
			if isEventStream {
				frames, decodeErr := decoder.Feed(buf[:n])
				if decodeErr != nil {
					reqLog.Warn("kiro_native.eventstream_decode_failed", zap.Error(decodeErr))
				}
				for i := range frames {
					usage.AddFrame(frames[i])
				}
			}
			if _, writeErr := c.Writer.Write(buf[:n]); writeErr != nil {
				clientDisconnected = true
				break
			}
			written += n
			c.Writer.Flush()
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				reqLog.Warn("kiro_native.upstream_read_failed", zap.Error(readErr))
			}
			break
		}
	}

	duration := time.Since(startTime)
	reqLog.Info("kiro_native.complete",
		zap.Int("status", resp.StatusCode),
		zap.Int("bytes", written),
		zap.Bool("client_disconnected", clientDisconnected),
		zap.Bool("event_stream", isEventStream),
		zap.Int("input_tokens", usage.InputTokens),
		zap.Int("output_tokens", usage.OutputTokens),
		zap.Int("cache_read_tokens", usage.CacheReadInputTokens),
		zap.Int("cache_write_tokens", usage.CacheCreationInputTokens),
		zap.Float64("metering_usage", usage.MeteringUsage),
		zap.Duration("duration", duration),
	)
	if usage.ErrorSeen {
		reqLog.Warn("kiro_native.upstream_error_event", zap.String("message", usage.ErrorMessage))
	}

	if isInference && resp.StatusCode < http.StatusBadRequest && usage.HasBillableTokens() && attributionAccount != nil {
		h.submitKiroNativeUsage(c, reqLog, billing, kiroNativeUsageInput{
			usage:          usage,
			attribution:    attributionAccount,
			apiKey:         apiKey,
			user:           user,
			model:          model,
			requestID:      firstNonEmpty(resp.Header.Get("x-amzn-requestid"), resp.Header.Get("x-request-id")),
			isStream:       isEventStream,
			duration:       duration,
			firstByteMs:    firstByteMs,
			disconnected:   clientDisconnected,
			inboundPath:    c.Request.URL.Path,
			upstreamURL:    targetURL,
			requestPayload: body,
			userAgent:      c.GetHeader("User-Agent"),
			clientIP:       ip.GetClientIP(c),
		})
	} else if isInference && resp.StatusCode < http.StatusBadRequest {
		reqLog.Warn("kiro_native.usage_missing",
			zap.String("model", model),
			zap.String("request_id", resp.Header.Get("x-amzn-requestid")),
			zap.Bool("token_usage_seen", usage.TokenUsageSeen),
			zap.Bool("metering_seen", usage.MeteringSeen),
		)
	}
}

type kiroNativeUsageInput struct {
	usage          kiroeventstream.Usage
	attribution    *service.Account
	apiKey         *service.APIKey
	user           *service.User
	model          string
	requestID      string
	isStream       bool
	duration       time.Duration
	firstByteMs    *int
	disconnected   bool
	inboundPath    string
	upstreamURL    string
	requestPayload []byte
	userAgent      string
	clientIP       string
}

func (h *GatewayHandler) submitKiroNativeUsage(c *gin.Context, reqLog *zap.Logger, billing kiroNativeBilling, in kiroNativeUsageInput) {
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	quotaPlatform := service.QuotaPlatform(c.Request.Context(), in.apiKey)
	inboundEndpoint := GetInboundEndpoint(c)
	if inboundEndpoint == "" {
		inboundEndpoint = kiroNativeRoutePrefix
	}
	upstreamEndpoint := kiroNativeUpstreamEndpoint(in.upstreamURL)
	sessionID := service.ExtractClientSessionID(c)
	if sessionID == "" {
		sessionID = kiroNativeConversationID(in.requestPayload)
	}
	requestPayloadHash := service.HashUsageRequestPayload(in.requestPayload)
	model := strings.TrimSpace(in.model)
	if model == "" {
		model = strings.TrimSpace(in.usage.Model)
	}
	if model == "" {
		model = "kiro-native"
	}

	result := &service.ForwardResult{
		RequestID: in.requestID,
		Usage: service.ClaudeUsage{
			InputTokens:              in.usage.InputTokens,
			OutputTokens:             in.usage.OutputTokens,
			CacheCreationInputTokens: in.usage.CacheCreationInputTokens,
			CacheReadInputTokens:     in.usage.CacheReadInputTokens,
		},
		Model:            model,
		Stream:           in.isStream,
		Duration:         in.duration,
		FirstTokenMs:     in.firstByteMs,
		ClientDisconnect: in.disconnected,
	}

	h.submitUsageRecordTask(c.Request.Context(), func(ctx context.Context) {
		if err := billing.RecordUsage(ctx, &service.RecordUsageInput{
			Result:             result,
			APIKey:             in.apiKey,
			User:               in.user,
			Account:            in.attribution,
			Subscription:       subscription,
			InboundEndpoint:    inboundEndpoint,
			UpstreamEndpoint:   upstreamEndpoint,
			UserAgent:          in.userAgent,
			IPAddress:          in.clientIP,
			SessionID:          sessionID,
			RequestPayloadHash: requestPayloadHash,
			APIKeyService:      h.apiKeyService,
			QuotaPlatform:      quotaPlatform,
		}); err != nil {
			logger.L().With(
				zap.String("component", kiroNativeComponent),
				zap.Int64("api_key_id", in.apiKey.ID),
				zap.Int64("account_id", in.attribution.ID),
				zap.String("model", model),
			).Error("kiro_native.record_usage_failed", zap.Error(err))
		}
	})
}

// kiroNativeRequestModel extracts the requested model from a native Kiro
// request body.
func kiroNativeRequestModel(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "conversationState.currentMessage.userInputMessage.modelId").String())
	if model == "" {
		model = strings.TrimSpace(gjson.GetBytes(body, "modelId").String())
	}
	return model
}

// kiroNativeConversationID extracts conversationState.conversationId for usage
// session correlation.
func kiroNativeConversationID(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(body, "conversationState.conversationId").String())
}

// kiroNativeUpstreamURL joins the configured upstream base with the client path
// suffix (everything after /kiro). The observed Kiro CLI/IDE traffic posts to
// the endpoint root, so "/" and "" both map to the configured upstream base;
// deeper suffixes are preserved for forward compatibility.
func kiroNativeUpstreamURL(base string, reqURL *url.URL) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", errors.New("empty upstream")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid upstream %q", base)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")

	suffix := ""
	rawQuery := ""
	if reqURL != nil {
		suffix = strings.TrimPrefix(reqURL.Path, kiroNativeRoutePrefix)
		suffix = strings.TrimPrefix(suffix, "/")
		rawQuery = reqURL.RawQuery
	}
	if suffix != "" {
		parsed.Path = parsed.Path + "/" + suffix
	}
	if rawQuery != "" {
		parsed.RawQuery = rawQuery
	}
	return parsed.String(), nil
}

// kiroNativeUpstreamEndpoint returns the path part of the upstream URL for
// usage_logs.upstream_endpoint.
func kiroNativeUpstreamEndpoint(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Path == "" {
		return "/"
	}
	return parsed.Path
}

func kiroNativeIsInferenceRequest(c *gin.Context, upstreamURL string) bool {
	if c != nil {
		if target := c.GetHeader("x-amz-target"); strings.Contains(target, "GenerateAssistantResponse") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(upstreamURL), "generateassistantresponse")
}

func isKiroNativeEventStreamResponse(header http.Header, isInference bool) bool {
	contentType := strings.ToLower(header.Get("Content-Type"))
	if strings.Contains(contentType, "vnd.amazon.eventstream") || strings.Contains(contentType, "event-stream") {
		return true
	}
	// Native inference always returns EventStream; fall back to the inference flag
	// so a missing/mistyped content-type still gets usage parsing (the decoder
	// simply finds no frames for JSON responses).
	return isInference
}

func copyKiroNativeRequestHeaders(src http.Header, dst http.Header) {
	if src == nil || dst == nil {
		return
	}
	connectionHeaderTokens := connectionTokens(src)
	for name, values := range src {
		lower := strings.ToLower(strings.TrimSpace(name))
		if isHopByHopHeader(lower) || connectionHeaderTokens[lower] {
			continue
		}
		switch lower {
		case "authorization", "x-api-key", "x-goog-api-key", "cookie", "host", "content-length", "accept-encoding":
			// Client credentials and hop-by-hop framing must not reach the inner
			// upstream; kiro2cc is authenticated with the X-Kiro-Native-Token header.
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func copyKiroNativeResponseHeaders(src http.Header, dst http.Header) {
	if src == nil || dst == nil {
		return
	}
	connectionHeaderTokens := connectionTokens(src)
	for name, values := range src {
		lower := strings.ToLower(strings.TrimSpace(name))
		if isHopByHopHeader(lower) || connectionHeaderTokens[lower] {
			continue
		}
		switch lower {
		case "content-length", "transfer-encoding":
			// The proxy streams the body and lets net/http frame it.
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

// connectionTokens returns the lower-cased header names listed in Connection.
func connectionTokens(header http.Header) map[string]bool {
	tokens := map[string]bool{}
	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			token = strings.ToLower(strings.TrimSpace(token))
			if token != "" {
				tokens[token] = true
			}
		}
	}
	return tokens
}

func isHopByHopHeader(lowerName string) bool {
	switch lowerName {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
