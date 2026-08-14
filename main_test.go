package main

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

func keyRune(r rune) *tcell.EventKey {
	return tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone)
}

func TestHandleNormalKeyMovesCursorAndOpensKillConfirm(t *testing.T) {
	s := newMonitorState()
	s.currentProcs = []Process{
		{PID: 1, Name: "init"},
		{PID: 2, Name: "bash"},
		{PID: 3, Name: "sleep"},
	}

	redraw, quit := handleNormalKey(s, tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if !redraw || quit {
		t.Fatalf("KeyDown: redraw=%v quit=%v, want redraw=true quit=false", redraw, quit)
	}
	if s.cursor != 1 {
		t.Errorf("cursor after KeyDown = %d, want 1", s.cursor)
	}

	redraw, quit = handleNormalKey(s, keyRune('x'))
	if !redraw || quit {
		t.Fatalf("'x': redraw=%v quit=%v, want redraw=true quit=false", redraw, quit)
	}
	if !s.killConfirmMode {
		t.Error("'x' did not open killConfirmMode")
	}
	if s.killTargetPID != 2 || s.killTargetName != "bash" {
		t.Errorf("killTarget = (%d, %q), want (2, \"bash\")", s.killTargetPID, s.killTargetName)
	}

	redraw, quit = handleNormalKey(s, keyRune('q'))
	if redraw || !quit {
		t.Errorf("'q': redraw=%v quit=%v, want redraw=false quit=true", redraw, quit)
	}
}

func TestHandleNormalKeyXWithNothingSelectedIsNoop(t *testing.T) {
	s := newMonitorState() // currentProcs is nil, cursor is 0

	redraw, quit := handleNormalKey(s, keyRune('x'))
	if redraw || quit {
		t.Errorf("'x' with empty process list: redraw=%v quit=%v, want both false", redraw, quit)
	}
	if s.killConfirmMode {
		t.Error("'x' with empty process list opened killConfirmMode")
	}
}

// TestHandleKillConfirmKeyBlocksStrayKeysAndCancels deliberately does not
// exercise 'y'/'Y' — those call syscall.Kill for real via sendKillSignal
// (state.go), and sending a real signal from the automated test suite would
// be flaky/platform-sensitive for no real coverage gain. Message formatting
// for that path is covered by TestKillResultMessage in state_test.go instead.
func TestHandleKillConfirmKeyBlocksStrayKeysAndCancels(t *testing.T) {
	s := newMonitorState()
	s.killConfirmMode = true
	s.killTargetPID = 12345
	s.killTargetName = "placeholder"

	redraw, quit := handleKillConfirmKey(s, keyRune('a'))
	if redraw || quit {
		t.Errorf("stray key 'a': redraw=%v quit=%v, want both false", redraw, quit)
	}
	if !s.killConfirmMode {
		t.Error("a stray keypress closed the kill-confirm prompt")
	}

	redraw, quit = handleKillConfirmKey(s, tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if !redraw || quit {
		t.Errorf("Escape: redraw=%v quit=%v, want redraw=true quit=false", redraw, quit)
	}
	if s.killConfirmMode {
		t.Error("Escape did not cancel killConfirmMode")
	}

	s.killConfirmMode = true
	redraw, quit = handleKillConfirmKey(s, keyRune('n'))
	if !redraw || quit {
		t.Errorf("'n': redraw=%v quit=%v, want redraw=true quit=false", redraw, quit)
	}
	if s.killConfirmMode {
		t.Error("'n' did not cancel killConfirmMode")
	}
}
