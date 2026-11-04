// Package kiroeventstream decodes the AWS EventStream framing used by the Kiro
// native protocol (application/vnd.amazon.eventstream) and extracts the usage
// events needed for billing.
//
// Frame layout (all integers big-endian):
//
//	[0:4]   total length (whole frame, including this field)
//	[4:8]   headers length
//	[8:12]  prelude CRC32 (IEEE) over bytes [0:8]
//	[12:12+headersLength] headers
//	[...:-4] payload (usually JSON)
//	[-4:]   message CRC32 (IEEE) over all preceding bytes
//
// Header entry encoding: [1B name length][name][1B value type][value], where
// value type 7 is a UTF-8 string prefixed by a 2-byte length, 6 is bytes with
// the same length prefix, and the numeric/bool types have fixed widths.
package kiroeventstream

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

const (
	preludeSize    = 12
	messageCRCSize = 4
	minMessageSize = preludeSize + messageCRCSize

	// DefaultMaxMessageSize is the largest single frame accepted by the decoder.
	DefaultMaxMessageSize = 16 << 20

	// DefaultMaxBufferSize bounds how much incomplete data the decoder buffers
	// before it gives up on the stream.
	DefaultMaxBufferSize = 32 << 20

	headerTypeString = 7
	headerTypeBytes  = 6
)

// AWS EventStream header names used by the Kiro protocol.
const (
	HeaderMessageType = ":message-type"
	HeaderEventType   = ":event-type"
	HeaderContentType = ":content-type"
	HeaderException   = ":exception-type"
	HeaderErrorCode   = ":error-code"
)

var (
	crc32Table = crc32.MakeTable(crc32.IEEE)

	errNeedMore  = errors.New("kiroeventstream: need more data")
	errMalformed = errors.New("kiroeventstream: malformed frame")
)

// Frame is one decoded EventStream message.
type Frame struct {
	Headers map[string]string
	Payload []byte
}

// Header returns a header value ("" when absent).
func (f Frame) Header(name string) string {
	return f.Headers[name]
}

// MessageType returns the ":message-type" header ("event", "error", ...).
func (f Frame) MessageType() string {
	return f.Headers[HeaderMessageType]
}

// EventType returns the ":event-type" header for event messages.
func (f Frame) EventType() string {
	return f.Headers[HeaderEventType]
}

// Decoder incrementally parses EventStream frames from a byte stream. Data may
// be fed in arbitrary chunks; partial frames stay buffered until the next call.
//
// Malformed frames (bad CRC / impossible lengths) are skipped byte-by-byte and
// counted in Dropped() so a corrupt prefix cannot wedge the proxy: the decoder
// resynchronizes on the next valid frame.
type Decoder struct {
	buf            []byte
	maxMessageSize int
	maxBufferSize  int
	dropped        uint64
}

// NewDecoder returns a decoder with the default frame/buffer limits.
func NewDecoder() *Decoder {
	return &Decoder{
		maxMessageSize: DefaultMaxMessageSize,
		maxBufferSize:  DefaultMaxBufferSize,
	}
}

// Dropped reports how many malformed bytes were skipped while resynchronizing.
func (d *Decoder) Dropped() uint64 {
	return d.dropped
}

// Buffered reports how many bytes are currently held for an incomplete frame.
func (d *Decoder) Buffered() int {
	return len(d.buf)
}

// Feed appends data and returns every complete frame now available.
//
// An error is returned only when the internal buffer exceeds the configured
// limit (a stream that never produces a valid frame); callers should then abort
// usage parsing while continuing to proxy bytes to the client.
func (d *Decoder) Feed(data []byte) ([]Frame, error) {
	if len(data) > 0 {
		d.buf = append(d.buf, data...)
	}
	if d.maxBufferSize > 0 && len(d.buf) > d.maxBufferSize {
		d.buf = nil
		return nil, fmt.Errorf("kiroeventstream: buffered data exceeds %d bytes", d.maxBufferSize)
	}

	var frames []Frame
	for len(d.buf) > 0 {
		frame, consumed, err := parseFrame(d.buf, d.maxMessageSize)
		switch {
		case err == nil:
			frames = append(frames, frame)
			d.buf = d.buf[consumed:]
		case errors.Is(err, errNeedMore):
			return frames, nil
		default:
			// Skip one byte to resynchronize; a truncated/corrupt prefix must not
			// stop us from finding later valid frames.
			d.dropped++
			d.buf = d.buf[1:]
		}
	}
	return frames, nil
}

// parseFrame decodes the first frame in buf. It returns errNeedMore when the
// buffer does not yet contain a full frame.
func parseFrame(buf []byte, maxMessageSize int) (Frame, int, error) {
	if len(buf) < preludeSize {
		return Frame{}, 0, errNeedMore
	}
	totalLength := int(binary.BigEndian.Uint32(buf[0:4]))
	headersLength := int(binary.BigEndian.Uint32(buf[4:8]))
	preludeCRC := binary.BigEndian.Uint32(buf[8:12])

	if totalLength < minMessageSize ||
		headersLength < 0 ||
		headersLength > totalLength-minMessageSize ||
		(maxMessageSize > 0 && totalLength > maxMessageSize) {
		return Frame{}, 0, errMalformed
	}
	if crc32.ChecksumIEEE(buf[0:8]) != preludeCRC {
		return Frame{}, 0, errMalformed
	}
	if len(buf) < totalLength {
		return Frame{}, 0, errNeedMore
	}
	messageCRC := binary.BigEndian.Uint32(buf[totalLength-messageCRCSize : totalLength])
	if crc32.ChecksumIEEE(buf[:totalLength-messageCRCSize]) != messageCRC {
		return Frame{}, 0, errMalformed
	}

	headerStart := preludeSize
	headerEnd := headerStart + headersLength
	payloadStart := headerEnd
	payloadEnd := totalLength - messageCRCSize

	headers, err := parseHeaders(buf[headerStart:headerEnd])
	if err != nil {
		return Frame{}, 0, errMalformed
	}
	payload := make([]byte, payloadEnd-payloadStart)
	copy(payload, buf[payloadStart:payloadEnd])
	return Frame{Headers: headers, Payload: payload}, totalLength, nil
}

func parseHeaders(raw []byte) (map[string]string, error) {
	headers := make(map[string]string, 4)
	pos := 0
	for pos < len(raw) {
		nameLength := int(raw[pos])
		pos++
		if nameLength == 0 || pos+nameLength > len(raw) {
			return nil, errMalformed
		}
		name := string(raw[pos : pos+nameLength])
		pos += nameLength

		if pos >= len(raw) {
			return nil, errMalformed
		}
		valueType := raw[pos]
		pos++

		switch valueType {
		case 0: // bool true
			headers[name] = "true"
		case 1: // bool false
			headers[name] = "false"
		case 2: // byte
			pos++
		case 3: // short
			pos += 2
		case 4: // int
			pos += 4
		case 5: // long
			pos += 8
		case headerTypeString, headerTypeBytes:
			if pos+2 > len(raw) {
				return nil, errMalformed
			}
			valueLength := int(binary.BigEndian.Uint16(raw[pos : pos+2]))
			pos += 2
			if pos+valueLength > len(raw) {
				return nil, errMalformed
			}
			if valueType == headerTypeString {
				headers[name] = string(raw[pos : pos+valueLength])
			}
			pos += valueLength
		case 8: // timestamp
			pos += 8
		case 9: // uuid
			pos += 16
		default:
			return nil, errMalformed
		}
		if pos > len(raw) {
			return nil, errMalformed
		}
	}
	return headers, nil
}

// EncodeFrame builds a single EventStream frame. It exists for tests and for
// future server-side emitters; the proxy path itself only decodes.
func EncodeFrame(headers map[string]string, payload []byte) []byte {
	headerBytes := encodeHeaders(headers)
	totalLength := preludeSize + len(headerBytes) + len(payload) + messageCRCSize
	frame := make([]byte, totalLength)
	binary.BigEndian.PutUint32(frame[0:4], uint32(totalLength))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(headerBytes)))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[0:8]))
	copy(frame[preludeSize:], headerBytes)
	copy(frame[preludeSize+len(headerBytes):], payload)
	binary.BigEndian.PutUint32(frame[totalLength-messageCRCSize:], crc32.ChecksumIEEE(frame[:totalLength-messageCRCSize]))
	return frame
}

func encodeHeaders(headers map[string]string) []byte {
	if len(headers) == 0 {
		return nil
	}
	// Deterministic order keeps encoded frames stable in tests.
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sortStrings(names)

	var out []byte
	for _, name := range names {
		value := headers[name]
		out = append(out, byte(len(name)))
		out = append(out, name...)
		out = append(out, headerTypeString)
		var length [2]byte
		binary.BigEndian.PutUint16(length[:], uint16(len(value)))
		out = append(out, length[:]...)
		out = append(out, value...)
	}
	return out
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
