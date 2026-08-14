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

func TestHandleEventClickSelectsRow(t *testing.T) {
	s := newMonitorState()
	s.currentProcs = []Process{
		{PID: 1, Name: "init"},
		{PID: 2, Name: "bash"},
		{PID: 3, Name: "sleep"},
	}
	s.tableTop = 10
	s.tableRowCount = 3
	s.cursor = 0

	click := tcell.NewEventMouse(4, 12, tcell.Button1, tcell.ModNone)
	redraw, quit := handleEvent(nil, s, click)
	if !redraw || quit {
		t.Fatalf("click on row: redraw=%v quit=%v, want redraw=true quit=false", redraw, quit)
	}
	if s.cursor != 2 {
		t.Errorf("cursor after click at y=12 = %d, want 2", s.cursor)
	}

	// A click outside the table body (e.g. the footer) is a no-op — it
	// shouldn't move the cursor or force a redraw.
	s.cursor = 1
	outside := tcell.NewEventMouse(4, 20, tcell.Button1, tcell.ModNone)
	redraw, quit = handleEvent(nil, s, outside)
	if redraw || quit {
		t.Errorf("click outside table: redraw=%v quit=%v, want both false", redraw, quit)
	}
	if s.cursor != 1 {
		t.Errorf("cursor after click outside table = %d, want unchanged 1", s.cursor)
	}
}

func TestHandleNormalKeyEnterOpensDetailView(t *testing.T) {
	s := newMonitorState()
	s.currentProcs = []Process{
		{PID: 1, Name: "init"},
		{PID: 2, Name: "bash"},
	}
	s.cursor = 1

	redraw, quit := handleNormalKey(s, tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if !redraw || quit {
		t.Fatalf("Enter: redraw=%v quit=%v, want redraw=true quit=false", redraw, quit)
	}
	if !s.detailMode {
		t.Error("Enter did not open detailMode")
	}
	if s.detailData.PID != 2 || s.detailData.Name != "bash" {
		t.Errorf("detailData = (%d, %q), want (2, \"bash\")", s.detailData.PID, s.detailData.Name)
	}
}

func TestHandleNormalKeyEnterWithNothingSelectedIsNoop(t *testing.T) {
	s := newMonitorState() // currentProcs is nil, cursor is 0

	redraw, quit := handleNormalKey(s, tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if redraw || quit {
		t.Errorf("Enter with empty process list: redraw=%v quit=%v, want both false", redraw, quit)
	}
	if s.detailMode {
		t.Error("Enter with empty process list opened detailMode")
	}
}

// TestHandleEventDispatchesDetailModeBeforeNormalKeys confirms handleEvent
// routes keys to handleDetailKey (not handleNormalKey) while detailMode is
// set — otherwise e.g. cursor movement or re-opening the popup would leak
// through, same concern TestHandleKillConfirmKeyBlocksStrayKeysAndCancels
// covers for killConfirmMode.
func TestHandleEventDispatchesDetailModeBeforeNormalKeys(t *testing.T) {
	s := newMonitorState()
	s.detailMode = true
	s.detailData = ProcessDetail{PID: 42, Name: "sleep"}

	redraw, quit := handleEvent(nil, s, tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if redraw || quit {
		t.Errorf("KeyDown while detailMode: redraw=%v quit=%v, want both false", redraw, quit)
	}
	if s.cursor != 0 {
		t.Errorf("cursor moved while detailMode was open: %d, want 0", s.cursor)
	}

	redraw, quit = handleEvent(nil, s, tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if !redraw || quit {
		t.Errorf("Escape while detailMode: redraw=%v quit=%v, want redraw=true quit=false", redraw, quit)
	}
	if s.detailMode {
		t.Error("Escape did not close detailMode")
	}
}

func TestHandleDetailKeyBlocksStrayKeysAndCloses(t *testing.T) {
	s := newMonitorState()
	s.detailMode = true

	redraw, quit := handleDetailKey(s, keyRune('a'))
	if redraw || quit {
		t.Errorf("stray key 'a': redraw=%v quit=%v, want both false", redraw, quit)
	}
	if !s.detailMode {
		t.Error("a stray keypress closed the detail popup")
	}

	redraw, quit = handleDetailKey(s, keyRune('q'))
	if !redraw || quit {
		t.Errorf("'q': redraw=%v quit=%v, want redraw=true quit=false", redraw, quit)
	}
	if s.detailMode {
		t.Error("'q' did not close detailMode")
	}

	s.detailMode = true
	redraw, quit = handleDetailKey(s, tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if !redraw || quit {
		t.Errorf("Enter: redraw=%v quit=%v, want redraw=true quit=false", redraw, quit)
	}
	if s.detailMode {
		t.Error("Enter did not close detailMode")
	}
}

func TestHandleNormalKeyOpensHelpView(t *testing.T) {
	for _, r := range []rune{'h', 'H', '?'} {
		s := newMonitorState()
		redraw, quit := handleNormalKey(s, keyRune(r))
		if !redraw || quit {
			t.Fatalf("%q: redraw=%v quit=%v, want redraw=true quit=false", r, redraw, quit)
		}
		if !s.helpMode {
			t.Errorf("%q did not open helpMode", r)
		}
	}
}

// TestHandleEventDispatchesHelpModeBeforeNormalKeys confirms handleEvent
// routes keys to handleHelpKey (not handleNormalKey) while helpMode is set,
// same concern TestHandleEventDispatchesDetailModeBeforeNormalKeys covers
// for detailMode.
func TestHandleEventDispatchesHelpModeBeforeNormalKeys(t *testing.T) {
	s := newMonitorState()
	s.helpMode = true

	redraw, quit := handleEvent(nil, s, tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if redraw || quit {
		t.Errorf("KeyDown while helpMode: redraw=%v quit=%v, want both false", redraw, quit)
	}
	if s.cursor != 0 {
		t.Errorf("cursor moved while helpMode was open: %d, want 0", s.cursor)
	}

	redraw, quit = handleEvent(nil, s, tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if !redraw || quit {
		t.Errorf("Escape while helpMode: redraw=%v quit=%v, want redraw=true quit=false", redraw, quit)
	}
	if s.helpMode {
		t.Error("Escape did not close helpMode")
	}
}

func TestHandleHelpKeyBlocksStrayKeysAndCloses(t *testing.T) {
	s := newMonitorState()
	s.helpMode = true

	redraw, quit := handleHelpKey(s, keyRune('a'))
	if redraw || quit {
		t.Errorf("stray key 'a': redraw=%v quit=%v, want both false", redraw, quit)
	}
	if !s.helpMode {
		t.Error("a stray keypress closed the help popup")
	}

	redraw, quit = handleHelpKey(s, keyRune('q'))
	if !redraw || quit {
		t.Errorf("'q': redraw=%v quit=%v, want redraw=true quit=false", redraw, quit)
	}
	if s.helpMode {
		t.Error("'q' did not close helpMode")
	}

	s.helpMode = true
	redraw, quit = handleHelpKey(s, tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if !redraw || quit {
		t.Errorf("Enter: redraw=%v quit=%v, want redraw=true quit=false", redraw, quit)
	}
	if s.helpMode {
		t.Error("Enter did not close helpMode")
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
