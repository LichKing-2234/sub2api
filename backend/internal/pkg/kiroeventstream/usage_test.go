package kiroeventstream

import "testing"

func feedEvent(t *testing.T, usage *Usage, eventType, payload string) {
	t.Helper()
	frames, err := NewDecoder().Feed(eventFrame(eventType, payload))
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	usage.AddFrame(frames[0])
}

func TestUsageAccumulatesMetadataEvent(t *testing.T) {
	var usage Usage
	feedEvent(t, &usage, EventMetadata, `{"stopReason":"end_turn","tokenUsage":{"uncachedInputTokens":11,"outputTokens":5,"totalTokens":16,"cacheReadInputTokens":2,"cacheWriteInputTokens":1}}`)

	if !usage.TokenUsageSeen {
		t.Fatal("TokenUsageSeen = false")
	}
	if usage.InputTokens != 11 || usage.OutputTokens != 5 {
		t.Errorf("input/output = %d/%d, want 11/5", usage.InputTokens, usage.OutputTokens)
	}
	if usage.CacheReadInputTokens != 2 || usage.CacheCreationInputTokens != 1 {
		t.Errorf("cache read/write = %d/%d, want 2/1", usage.CacheReadInputTokens, usage.CacheCreationInputTokens)
	}
	if usage.TotalTokens != 16 {
		t.Errorf("total = %d, want 16", usage.TotalTokens)
	}
	if usage.StopReason != "end_turn" {
		t.Errorf("stop reason = %q", usage.StopReason)
	}
	if !usage.HasBillableTokens() {
		t.Error("HasBillableTokens = false")
	}
}

func TestUsageMetadataEventWithoutTokenUsageIsNotBillable(t *testing.T) {
	var usage Usage
	feedEvent(t, &usage, EventMetadata, `{"stopReason":"end_turn"}`)
	if usage.TokenUsageSeen {
		t.Error("TokenUsageSeen = true, want false")
	}
	if usage.HasBillableTokens() {
		t.Error("HasBillableTokens = true, want false")
	}
	if usage.StopReason != "end_turn" {
		t.Errorf("stop reason = %q", usage.StopReason)
	}
}

func TestUsageFallsBackToAlternateTokenFields(t *testing.T) {
	var usage Usage
	feedEvent(t, &usage, EventMetadata, `{"tokenUsage":{"inputTokens":20,"outputTokens":7,"cacheCreationInputTokens":3}}`)
	if usage.InputTokens != 20 || usage.OutputTokens != 7 || usage.CacheCreationInputTokens != 3 {
		t.Errorf("tokens = %d/%d/%d", usage.InputTokens, usage.OutputTokens, usage.CacheCreationInputTokens)
	}
	if usage.TotalTokens != 30 {
		t.Errorf("derived total = %d, want 30", usage.TotalTokens)
	}
}

func TestUsageDerivesUncachedInputFromTotal(t *testing.T) {
	var usage Usage
	feedEvent(t, &usage, EventMetadata, `{"tokenUsage":{"totalTokens":100,"outputTokens":10,"cacheReadInputTokens":40,"cacheWriteInputTokens":5}}`)
	if usage.InputTokens != 45 {
		t.Errorf("input = %d, want 45", usage.InputTokens)
	}
	if usage.TotalTokens != 100 {
		t.Errorf("total = %d, want 100", usage.TotalTokens)
	}
}

func TestUsageLastMetadataEventWins(t *testing.T) {
	var usage Usage
	feedEvent(t, &usage, EventMetadata, `{"tokenUsage":{"uncachedInputTokens":5,"outputTokens":2,"totalTokens":7}}`)
	feedEvent(t, &usage, EventMetadata, `{"tokenUsage":{"uncachedInputTokens":9,"outputTokens":4,"totalTokens":13}}`)
	if usage.InputTokens != 9 || usage.OutputTokens != 4 || usage.TotalTokens != 13 {
		t.Errorf("tokens = %d/%d/%d, want 9/4/13", usage.InputTokens, usage.OutputTokens, usage.TotalTokens)
	}
}

func TestUsageMeteringAndContextEvents(t *testing.T) {
	var usage Usage
	feedEvent(t, &usage, EventMetering, `{"unit":"credit","unitPlural":"credits","usage":0.01}`)
	feedEvent(t, &usage, EventContextUsage, `{"contextUsagePercentage":12.5}`)

	if !usage.MeteringSeen || usage.MeteringUsage != 0.01 || usage.MeteringUnit != "credit" {
		t.Errorf("metering = %+v", usage)
	}
	if !usage.ContextUsageSeen || usage.ContextUsagePercentage != 12.5 {
		t.Errorf("context usage = %+v", usage)
	}
	if usage.HasBillableTokens() {
		t.Error("HasBillableTokens = true without token usage")
	}
}

func TestUsageCapturesAssistantModel(t *testing.T) {
	var usage Usage
	feedEvent(t, &usage, EventAssistant, `{"content":"hello","modelId":"claude-sonnet-4.6"}`)
	if usage.Model != "claude-sonnet-4.6" {
		t.Errorf("model = %q", usage.Model)
	}
}

func TestUsageCapturesErrorEvent(t *testing.T) {
	var usage Usage
	frames, err := NewDecoder().Feed(EncodeFrame(map[string]string{
		HeaderMessageType: "error",
		HeaderErrorCode:   "ThrottlingException",
	}, []byte(`{"message":"slow down"}`)))
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	usage.AddFrame(frames[0])
	if !usage.ErrorSeen || usage.ErrorMessage != "slow down" {
		t.Errorf("error = %+v", usage)
	}
}

func TestUsageIgnoresUnknownEventAndMalformedPayload(t *testing.T) {
	var usage Usage
	feedEvent(t, &usage, "someFutureEvent", `{"whatever":true}`)
	feedEvent(t, &usage, EventMetadata, `{not-json`)
	if usage.HasBillableTokens() {
		t.Error("HasBillableTokens = true, want false")
	}
	if usage.StopReason != "" {
		t.Errorf("stop reason = %q", usage.StopReason)
	}
}
