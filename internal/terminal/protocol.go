package terminal

import (
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
)

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
