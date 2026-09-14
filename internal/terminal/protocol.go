package terminal

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
)

// Protocols defines the name of this protocol,
// which is supposed to be used as the subprotocol of WebSocket streams.
var Protocols = []string{"webtty"}

// Message types sent from the client to the server.
const (
	// UnknownInput message type, maybe sent by a bug.
	UnknownInput = '0'
	// Input is user input, typically from a keyboard.
	Input = '1'
	// Ping is a keep-alive message from the client.
	Ping = '2'
	// ResizeTerminal notifies the server that the terminal size has changed.
	ResizeTerminal = '3'
)

// Message types sent from the server to the client.
const (
	// UnknownOutput message type, maybe set by a bug.
	UnknownOutput = '0'
	// Output is normal terminal output (raw bytes, no base64).
	Output = '1'
	// Pong is the response to a client Ping.
	Pong = '2'
	// SetWindowTitle sets the window title of the terminal.
	SetWindowTitle = '3'
	// SetPreferences sets terminal preferences.
	SetPreferences = '4'
	// SetReconnect tells the client to reconnect after disconnection.
	SetReconnect = '5'
	// SetReplayDone is sent right after the attach-time init frames. It is
	// the handshake marker after which the client may forward input: xterm
	// auto-generates answers for terminal queries (DSR/DECRQM/OSC) it sees
	// in the output stream, and those answers must NOT be written back into
	// the PTY — the program that issued the queries is not waiting for them.
	// (Named for the historical attach-time output replay; the marker itself
	// is still what gates input forwarding in the browser.)
	SetReplayDone = '6'
	// MirrorDiff carries the changed screen rows for a read-only monitor
	// subscription (`GET /ws?...&mode=mirror`). It is only ever sent on a
	// mirror connection, never on an attached one: a monitor reads the
	// screen mirror instead of the PTY stream and must not preempt the
	// attached client. Payload is a JSON MirrorDiffFrame.
	MirrorDiff = '7'
)

// Multiplexed WebSocket routing message types (ws-multiplex.md). A single
// WebSocket connection carries many logical channels, each scoped to a
// session by a 16-byte id in the routing header. These types are layered on
// top of the session-level types above and never change their byte values.
const (
	// Attach (C→S) binds the client's logical channel to the session named in
	// the routing header; the server streams init frames once it replies
	// AttachOK. A second attach to the same session preempts the first.
	Attach = 'A'
	// Detach (C→S) releases the client's logical channel without destroying
	// the session (the session keeps running on the server).
	Detach = 'D'
	// AttachOK (S→C) confirms an attach succeeded; init frames (title,
	// prefs, replay) follow on this channel.
	AttachOK = 'a'
	// AttachErr (S→C) rejects an attach (unknown or destroyed session); the
	// payload is a human-readable reason phrase.
	AttachErr = 'b'
	// Event (S→C) carries a 1-byte session status change on a logical
	// channel (see Event* status codes below).
	Event = 'E'

	// EventPreempted is the Event payload when another client took over the
	// session: the channel should show "session taken over" and NOT auto
	// reconnect, to avoid a preemption ping-pong.
	EventPreempted byte = 0x01
	// EventDestroyed is the Event payload when the session was destroyed
	// (REST DELETE): the channel should show "session destroyed".
	EventDestroyed byte = 0x02
)

// Routing header constants for the multiplexed WebSocket protocol.
const (
	// RouteSessionIDLen is the fixed size of the session id field in a routed
	// frame. Server-generated ids are exactly 16 base36 chars; an all-zero id
	// marks a connection-level (not session-scoped) message such as a ping.
	RouteSessionIDLen = 16
	// RouteHeaderLen = session id (16) + type (1) + length (2, big-endian).
	RouteHeaderLen = RouteSessionIDLen + 1 + 2
	// MaxRoutePayload bounds a single routed payload to the 2-byte length
	// field. Terminal frames are chunked to 32 KiB, so this is never hit in
	// practice.
	MaxRoutePayload = 65535
)

// MirrorDiffFrame is the payload of a MirrorDiff frame — one monitor update.
//
// Lines holds only the rows that changed since the previous update (dirty-row
// diff), except on the first frame and after a resize, which are full.
type MirrorDiffFrame struct {
	SessionID string `json:"session_id"`
	Version   uint64 `json:"version"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
	// Full is true when Lines covers the whole screen (first frame or after
	// a resize); a client must drop any state it had for rows not listed.
	Full   bool             `json:"full"`
	Cursor MirrorCursor     `json:"cursor"`
	Lines  []MirrorDiffLine `json:"lines"`
}

// MirrorDiffLine is one changed row in a MirrorDiffFrame.
type MirrorDiffLine struct {
	Row  int    `json:"row"`
	Text string `json:"text"`
}

// MirrorCursor is the mirror-tracked cursor reported with a diff.
type MirrorCursor struct {
	Row     int  `json:"row"`
	Col     int  `json:"col"`
	Visible bool `json:"visible"`
}

// EncodeFrame wraps payload with a message type byte:
// [type byte] [payload bytes...]
func EncodeFrame(msgType byte, payload []byte) []byte {
	frame := make([]byte, 1+len(payload))
	frame[0] = msgType
	copy(frame[1:], payload)
	return frame
}

// EncodeOutput builds an Output frame carrying raw terminal output.
func EncodeOutput(payload []byte) []byte {
	return EncodeFrame(Output, payload)
}

// EncodePong builds a Pong frame.
func EncodePong() []byte {
	return []byte{Pong}
}

// EncodeWindowTitle builds a SetWindowTitle frame.
func EncodeWindowTitle(title []byte) []byte {
	return EncodeFrame(SetWindowTitle, title)
}

// EncodePreferences builds a SetPreferences frame.
func EncodePreferences(prefs []byte) []byte {
	return EncodeFrame(SetPreferences, prefs)
}

// EncodeReconnect builds a SetReconnect frame whose payload is a JSON number.
func EncodeReconnect(seconds int) []byte {
	payload, _ := json.Marshal(seconds)
	return EncodeFrame(SetReconnect, payload)
}

// EncodeReplayDone builds an empty-byte SetReplayDone frame.
func EncodeReplayDone() []byte {
	return []byte{SetReplayDone}
}

// EncodeMirrorDiff builds a MirrorDiff frame: the type byte followed by the
// JSON payload, so the frame stays a binary WebSocket message while the
// payload remains inspectable from a shell (and cheap to decode in JS).
func EncodeMirrorDiff(frame MirrorDiffFrame) ([]byte, error) {
	payload, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	return EncodeFrame(MirrorDiff, payload), nil
}

// DecodeMirrorDiff parses the payload of a MirrorDiff frame.
func DecodeMirrorDiff(payload []byte) (MirrorDiffFrame, error) {
	var frame MirrorDiffFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		return MirrorDiffFrame{}, fmt.Errorf("invalid mirror diff payload: %w", err)
	}
	return frame, nil
}

// ClientMessage is a decoded frame received from the client.
type ClientMessage struct {
	Type    byte
	Payload []byte
}

// DecodeClientFrame parses a frame received from the client.
// An empty frame is invalid: both Ping and ResizeTerminal are
// distinguished by their type byte, and Input requires a payload.
func DecodeClientFrame(frame []byte) (ClientMessage, error) {
	if len(frame) == 0 {
		return ClientMessage{}, fmt.Errorf("%w: empty frame", ErrInvalidMessage)
	}

	switch frame[0] {
	case Input, Ping, ResizeTerminal:
		return ClientMessage{Type: frame[0], Payload: frame[1:]}, nil
	default:
		return ClientMessage{}, fmt.Errorf("%w: unknown message type `%c`", ErrInvalidMessage, frame[0])
	}
}

// ResizeArgs is the JSON payload of a ResizeTerminal message.
// encoding/json matches keys case-insensitively, so both
// `{"columns":80,"rows":24}` and `{"Columns":80,"Rows":24}` are accepted.
type ResizeArgs struct {
	Columns int `json:"columns"`
	Rows    int `json:"rows"`
}

// ParseResizeArgs decodes the JSON payload of a ResizeTerminal message.
func ParseResizeArgs(payload []byte) (ResizeArgs, error) {
	var args ResizeArgs
	if err := json.Unmarshal(payload, &args); err != nil {
		return ResizeArgs{}, fmt.Errorf("%w: invalid resize payload: %v", ErrInvalidMessage, err)
	}
	return args, nil
}

// EncodeRouteFrame wraps a session message with the routing header:
//
//	[sessionID (16B)] [type (1B)] [len (2B, big-endian)] [payload]
//
// sessionID must be exactly RouteSessionIDLen bytes; an all-zero id is the
// connection-level marker. Payloads longer than MaxRoutePayload are rejected
// (terminal frames are chunked well under the limit).
func EncodeRouteFrame(sessionID []byte, msgType byte, payload []byte) ([]byte, error) {
	if len(sessionID) != RouteSessionIDLen {
		return nil, fmt.Errorf("%w: session id must be %d bytes, got %d", ErrInvalidMessage, RouteSessionIDLen, len(sessionID))
	}
	if len(payload) > MaxRoutePayload {
		return nil, fmt.Errorf("%w: payload %d exceeds max %d", ErrInvalidMessage, len(payload), MaxRoutePayload)
	}
	frame := make([]byte, RouteHeaderLen+len(payload))
	copy(frame[:RouteSessionIDLen], sessionID)
	frame[RouteSessionIDLen] = msgType
	binary.BigEndian.PutUint16(frame[RouteSessionIDLen+1:], uint16(len(payload)))
	copy(frame[RouteHeaderLen:], payload)
	return frame, nil
}

// DecodeRouteFrame parses a routed frame into its session id and inner
// message. An error is returned for a frame shorter than the header or whose
// declared length disagrees with the actual payload.
func DecodeRouteFrame(frame []byte) (sessionID []byte, msg ClientMessage, err error) {
	if len(frame) < RouteHeaderLen {
		return nil, ClientMessage{}, fmt.Errorf("%w: routed frame too short (%d < %d)", ErrInvalidMessage, len(frame), RouteHeaderLen)
	}
	sid := make([]byte, RouteSessionIDLen)
	copy(sid, frame[:RouteSessionIDLen])
	msgType := frame[RouteSessionIDLen]
	plen := int(binary.BigEndian.Uint16(frame[RouteSessionIDLen+1 : RouteSessionIDLen+3]))
	payload := frame[RouteHeaderLen:]
	if len(payload) != plen {
		return nil, ClientMessage{}, fmt.Errorf("%w: routed payload length %d != declared %d", ErrInvalidMessage, len(payload), plen)
	}
	return sid, ClientMessage{Type: msgType, Payload: payload}, nil
}

// IsConnectionLevel reports whether a session id is the all-zero connection
// level marker (heartbeat / RTT probe), i.e. not scoped to any session.
func IsConnectionLevel(sessionID []byte) bool {
	for _, b := range sessionID {
		if b != 0 {
			return false
		}
	}
	return true
}
