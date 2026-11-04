package kiroeventstream

import (
	"encoding/json"
	"strings"
)

// Kiro native event types that carry billing-relevant data.
const (
	EventMetadata        = "metadataEvent"
	EventMetering        = "meteringEvent"
	EventContextUsage    = "contextUsageEvent"
	EventAssistant       = "assistantResponseEvent"
	MessageTypeEvent     = "event"
	MessageTypeError     = "error"
	MessageTypeException = "exception"
)

// TokenUsage mirrors metadataEvent.tokenUsage as emitted by the Kiro backend.
//
// Observed field names (Kiro CLI v2, verified against a live client):
//
//	{"stopReason":"end_turn",
//	 "tokenUsage":{"uncachedInputTokens":11,"outputTokens":5,"totalTokens":16,
//	               "cacheReadInputTokens":0,"cacheWriteInputTokens":0}}
//
// inputTokens / cacheCreationInputTokens are tolerated as alternates because a
// future Kiro version may rename them; the first non-nil wins.
type TokenUsage struct {
	UncachedInputTokens      *int64 `json:"uncachedInputTokens"`
	InputTokens              *int64 `json:"inputTokens"`
	OutputTokens             *int64 `json:"outputTokens"`
	TotalTokens              *int64 `json:"totalTokens"`
	CacheReadInputTokens     *int64 `json:"cacheReadInputTokens"`
	CacheWriteInputTokens    *int64 `json:"cacheWriteInputTokens"`
	CacheCreationInputTokens *int64 `json:"cacheCreationInputTokens"`
}

type metadataEventPayload struct {
	StopReason string      `json:"stopReason"`
	TokenUsage *TokenUsage `json:"tokenUsage"`
}

type meteringEventPayload struct {
	Unit       string  `json:"unit"`
	UnitPlural string  `json:"unitPlural"`
	Usage      float64 `json:"usage"`
}

type contextUsageEventPayload struct {
	ContextUsagePercentage float64 `json:"contextUsagePercentage"`
}

type assistantResponseEventPayload struct {
	Content string `json:"content"`
	ModelID string `json:"modelId"`
}

// Usage accumulates billing-relevant values from a Kiro native EventStream.
type Usage struct {
	// Token counters (Anthropic usage semantics: input excludes cache read/write).
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
	TotalTokens              int
	// TokenUsageSeen reports whether a metadataEvent carrying tokenUsage arrived.
	TokenUsageSeen bool
	// StopReason is the last metadataEvent.stopReason observed.
	StopReason string
	// Model is the last model id reported by an assistantResponseEvent.
	Model string

	// Metering mirrors meteringEvent (Kiro credits).
	MeteringUsage      float64
	MeteringUnit       string
	MeteringUnitPlural string
	MeteringSeen       bool

	// ContextUsagePercentage mirrors contextUsageEvent.
	ContextUsagePercentage float64
	ContextUsageSeen       bool

	// ErrorMessage captures a native error/exception frame for logging.
	ErrorMessage string
	ErrorSeen    bool
}

// AddFrame consumes one decoded frame and updates the accumulator. Unknown
// events and unparsable payloads are ignored on purpose: usage accounting must
// never break the proxied stream.
func (u *Usage) AddFrame(frame Frame) {
	switch frame.MessageType() {
	case MessageTypeError, MessageTypeException:
		u.ErrorSeen = true
		u.ErrorMessage = errorMessageFromPayload(frame.Payload)
		return
	case MessageTypeEvent:
	default:
		return
	}

	switch frame.EventType() {
	case EventMetadata:
		var payload metadataEventPayload
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			return
		}
		if payload.StopReason != "" {
			u.StopReason = payload.StopReason
		}
		u.applyTokenUsage(payload.TokenUsage)
	case EventMetering:
		var payload meteringEventPayload
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			return
		}
		u.MeteringSeen = true
		u.MeteringUsage = payload.Usage
		if payload.Unit != "" {
			u.MeteringUnit = payload.Unit
		}
		if payload.UnitPlural != "" {
			u.MeteringUnitPlural = payload.UnitPlural
		}
	case EventContextUsage:
		var payload contextUsageEventPayload
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			return
		}
		u.ContextUsageSeen = true
		u.ContextUsagePercentage = payload.ContextUsagePercentage
	case EventAssistant:
		var payload assistantResponseEventPayload
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			return
		}
		if strings.TrimSpace(payload.ModelID) != "" {
			u.Model = strings.TrimSpace(payload.ModelID)
		}
	}
}

func (u *Usage) applyTokenUsage(tokenUsage *TokenUsage) {
	if tokenUsage == nil {
		return
	}
	u.TokenUsageSeen = true

	input := firstInt64(tokenUsage.UncachedInputTokens, tokenUsage.InputTokens)
	output := firstInt64(tokenUsage.OutputTokens)
	cacheRead := firstInt64(tokenUsage.CacheReadInputTokens)
	cacheWrite := firstInt64(tokenUsage.CacheWriteInputTokens, tokenUsage.CacheCreationInputTokens)
	total := firstInt64(tokenUsage.TotalTokens)

	if input != nil {
		u.InputTokens = int(*input)
	}
	if output != nil {
		u.OutputTokens = int(*output)
	}
	if cacheRead != nil {
		u.CacheReadInputTokens = int(*cacheRead)
	}
	if cacheWrite != nil {
		u.CacheCreationInputTokens = int(*cacheWrite)
	}
	if total != nil {
		u.TotalTokens = int(*total)
	} else {
		u.TotalTokens = u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	}
	// Input accounting fallback: some upstreams omit uncachedInputTokens and only
	// report totalTokens; derive the uncached remainder so billing is not zeroed.
	if input == nil && total != nil {
		derived := int(*total) - u.OutputTokens - u.CacheReadInputTokens - u.CacheCreationInputTokens
		if derived > 0 {
			u.InputTokens = derived
		}
	}
}

// HasBillableTokens reports whether a usage row should be written.
func (u *Usage) HasBillableTokens() bool {
	if u == nil {
		return false
	}
	return u.TokenUsageSeen || u.InputTokens > 0 || u.OutputTokens > 0 ||
		u.CacheReadInputTokens > 0 || u.CacheCreationInputTokens > 0
}

func firstInt64(values ...*int64) *int64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func errorMessageFromPayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var generic map[string]any
	if err := json.Unmarshal(payload, &generic); err != nil {
		return ""
	}
	for _, key := range []string{"message", "errorMessage", "Message", "error_description"} {
		if value, ok := generic[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
