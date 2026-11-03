package terminal

import (
	"testing"
)

// M4 attached 引擎的帧编解码:请求帧、分片结果帧,以及客户端帧解析必须接受
// CaptureResult('c')——否则浏览器回传的结果会在 DecodeClientFrame 处被丢弃。

func TestCaptureRequestRoundTrip(t *testing.T) {
	frame, err := EncodeCaptureRequest(CaptureRequestFrame{IncludeSelection: true})
	if err != nil {
		t.Fatalf("EncodeCaptureRequest: %v", err)
	}
	if frame[0] != CaptureRequest {
		t.Fatalf("frame type = %q, want %q", frame[0], CaptureRequest)
	}

	got, err := DecodeCaptureRequest(frame[1:])
	if err != nil {
		t.Fatalf("DecodeCaptureRequest: %v", err)
	}
	if !got.IncludeSelection {
		t.Error("include_selection lost in round trip")
	}
}

func TestCaptureRequestEmptyPayloadIsZeroValue(t *testing.T) {
	// 空 payload 是合法的默认请求(所有选项都取默认值)。
	got, err := DecodeCaptureRequest(nil)
	if err != nil {
		t.Fatalf("DecodeCaptureRequest(nil): %v", err)
	}
	if got.IncludeSelection {
		t.Error("zero payload must decode to the zero request")
	}
}

func TestCaptureResultChunkRoundTrip(t *testing.T) {
	want := CaptureResultFrame{Seq: 2, Total: 5, Data: "QUJD", Cols: 120, Rows: 30}
	frame, err := EncodeCaptureResult(want)
	if err != nil {
		t.Fatalf("EncodeCaptureResult: %v", err)
	}
	if frame[0] != CaptureResult {
		t.Fatalf("frame type = %q, want %q", frame[0], CaptureResult)
	}

	got, err := DecodeCaptureResult(frame[1:])
	if err != nil {
		t.Fatalf("DecodeCaptureResult: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestDecodeClientFrameAcceptsCaptureResult(t *testing.T) {
	// 浏览器回传的结果帧必须被客户端帧解析接受,否则 masterToSlave 会把它
	// 当成非法帧断开附着。
	msg, err := DecodeClientFrame([]byte{CaptureResult, 'x'})
	if err != nil {
		t.Fatalf("DecodeClientFrame: %v", err)
	}
	if msg.Type != CaptureResult {
		t.Errorf("type = %q, want %q", msg.Type, CaptureResult)
	}
	if string(msg.Payload) != "x" {
		t.Errorf("payload = %q, want %q", msg.Payload, "x")
	}
}

func TestDecodeClientFrameStillRejectsUnknown(t *testing.T) {
	if _, err := DecodeClientFrame([]byte{'Z'}); err == nil {
		t.Fatal("unknown frame type must be rejected")
	}
}
