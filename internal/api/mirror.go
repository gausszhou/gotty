package api

import (
	"github.com/gausszhou/gotty/internal/capture"
	"github.com/gausszhou/gotty/internal/session"
)

// captureMirror adapts a capture.Emulator to the session.ScreenMirror
// interface. session must not import capture — the capture package's
// browser engine depends on session, and a session → capture edge would
// create an import cycle — so the adapter lives here in the api layer,
// which imports both.
type captureMirror struct {
	emu *capture.Emulator
}

// MirrorFactory returns the session.MirrorFactory that wires the capture
// emulator in as the per-session screen mirror. A disabled mirror
// (--mirror=false) yields a nil factory, so sessions never allocate the
// grid and the screen/wait endpoints answer 503.
func MirrorFactory(enabled bool) session.MirrorFactory {
	if !enabled {
		return nil
	}
	return func(term session.Terminal) session.ScreenMirror {
		cols, rows := 80, 24 // mirror 默认尺寸;首个 resize 会同步
		if sz, ok := term.(interface{ Size() (int, int) }); ok {
			if c, r := sz.Size(); c > 0 && r > 0 {
				cols, rows = c, r
			}
		}
		return &captureMirror{emu: capture.NewEmulator(cols, rows)}
	}
}

func (m *captureMirror) Write(p []byte) (int, error) { return m.emu.Write(p) }

func (m *captureMirror) Resize(cols, rows int) { m.emu.Resize(cols, rows) }

func (m *captureMirror) DrainAnswers() []byte { return m.emu.DrainAnswers() }

func (m *captureMirror) Snapshot() session.ScreenSnapshot {
	snap := m.emu.Snapshot()
	return session.ScreenSnapshot{Text: snap.Text(), Raw: snap}
}
