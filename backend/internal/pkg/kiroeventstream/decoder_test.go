package kiroeventstream

import (
	"encoding/binary"
	"strings"
	"testing"
)

func eventFrame(eventType string, payload string) []byte {
	return EncodeFrame(map[string]string{
		HeaderMessageType: "event",
		HeaderEventType:   eventType,
		HeaderContentType: "application/json",
	}, []byte(payload))
}

func TestDecoderSingleFrame(t *testing.T) {
	frameBytes := eventFrame("assistantResponseEvent", `{"content":"hi"}`)

	decoder := NewDecoder()
	frames, err := decoder.Feed(frameBytes)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	got := frames[0]
	if got.MessageType() != "event" {
		t.Errorf("message type = %q", got.MessageType())
	}
	if got.EventType() != "assistantResponseEvent" {
		t.Errorf("event type = %q", got.EventType())
	}
	if got.Header(HeaderContentType) != "application/json" {
		t.Errorf("content type = %q", got.Header(HeaderContentType))
	}
	if string(got.Payload) != `{"content":"hi"}` {
		t.Errorf("payload = %q", got.Payload)
	}
	if decoder.Buffered() != 0 {
		t.Errorf("buffered = %d, want 0", decoder.Buffered())
	}
}

func TestDecoderPartialFrameAcrossFeeds(t *testing.T) {
	frameBytes := eventFrame("metadataEvent", `{"stopReason":"end_turn"}`)
	decoder := NewDecoder()

	var frames []Frame
	for i := 0; i < len(frameBytes); i++ {
		got, err := decoder.Feed(frameBytes[i : i+1])
		if err != nil {
			t.Fatalf("Feed byte %d: %v", i, err)
		}
		frames = append(frames, got...)
		if i < len(frameBytes)-1 && len(frames) != 0 {
			t.Fatalf("frame emitted early at byte %d", i)
		}
	}
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	if frames[0].EventType() != "metadataEvent" {
		t.Errorf("event type = %q", frames[0].EventType())
	}
}

func TestDecoderMultipleFramesOneFeed(t *testing.T) {
	var data []byte
	data = append(data, eventFrame("assistantResponseEvent", `{"content":"a"}`)...)
	data = append(data, eventFrame("assistantResponseEvent", `{"content":"b"}`)...)
	data = append(data, eventFrame("metadataEvent", `{"stopReason":"end_turn"}`)...)

	frames, err := NewDecoder().Feed(data)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want 3", len(frames))
	}
}

func TestDecoderSkipsCorruptFrameAndResyncs(t *testing.T) {
	corrupt := eventFrame("assistantResponseEvent", `{"content":"bad"}`)
	corrupt[len(corrupt)-6] ^= 0xFF // break message CRC
	valid := eventFrame("metadataEvent", `{"stopReason":"end_turn"}`)

	decoder := NewDecoder()
	frames, err := decoder.Feed(append(corrupt, valid...))
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1 (corrupt skipped)", len(frames))
	}
	if frames[0].EventType() != "metadataEvent" {
		t.Errorf("event type = %q", frames[0].EventType())
	}
	if decoder.Dropped() == 0 {
		t.Error("Dropped() = 0, want > 0")
	}
}

func TestDecoderSkipsImpossibleLengthPrefix(t *testing.T) {
	garbage := make([]byte, 16)
	binary.BigEndian.PutUint32(garbage[0:4], 0) // total length below minimum
	valid := eventFrame("assistantResponseEvent", `{"content":"ok"}`)

	decoder := NewDecoder()
	frames, err := decoder.Feed(append(garbage, valid...))
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	if decoder.Dropped() == 0 {
		t.Error("Dropped() = 0, want > 0")
	}
}

func TestDecoderIgnoresTrailingPartialBytes(t *testing.T) {
	frameBytes := eventFrame("assistantResponseEvent", `{"content":"ok"}`)
	decoder := NewDecoder()
	frames, err := decoder.Feed(frameBytes[:len(frameBytes)-3])
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(frames) != 0 {
		t.Fatalf("frames = %d, want 0", len(frames))
	}
	if decoder.Buffered() != len(frameBytes)-3 {
		t.Errorf("buffered = %d", decoder.Buffered())
	}
}

func TestEncoderProducesParseableHeaders(t *testing.T) {
	frameBytes := EncodeFrame(map[string]string{
		HeaderMessageType: "error",
		HeaderErrorCode:   "ThrottlingException",
	}, []byte(`{"message":"slow down"}`))

	frames, err := NewDecoder().Feed(frameBytes)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	if frames[0].Header(HeaderErrorCode) != "ThrottlingException" {
		t.Errorf("error code = %q", frames[0].Header(HeaderErrorCode))
	}
	if !strings.Contains(string(frames[0].Payload), "slow down") {
		t.Errorf("payload = %q", frames[0].Payload)
	}
}

func TestDecoderBufferLimitReturnsError(t *testing.T) {
	decoder := &Decoder{maxMessageSize: DefaultMaxMessageSize, maxBufferSize: 8}
	if _, err := decoder.Feed([]byte("more than eight bytes")); err == nil {
		t.Fatal("Feed error = nil, want buffer-limit error")
	}
	if decoder.Buffered() != 0 {
		t.Errorf("buffered = %d, want 0 after fatal error", decoder.Buffered())
	}
}
