package gocui

import (
	"testing"

	"github.com/gdamore/tcell/v3"
	"github.com/stretchr/testify/assert"
)

// advancedKeyEvent makes a key press as tcell reports it with advanced keys on.
func advancedKeyEvent(k tcell.Key, str string, mod tcell.ModMask) *tcell.EventKey {
	return tcell.NewEventKeyEx(k, str, mod, true, 0, 1)
}

// eventWithBindingKey converts tev as pollEvent does for a terminal that
// reports Shift with text keys.
func eventWithBindingKey(tev tcell.Event) GocuiEvent {
	ev := gocuiEventFromTcellEvent(tev)
	ev.bindingKey = caseFromShift(&ev)
	return ev
}

func TestCapsLockLetterMatchesLowercaseBinding(t *testing.T) {
	ev := eventWithBindingKey(advancedKeyEvent(tcell.KeyRune, "P", tcell.ModNone))

	assert.Equal(t, eventKey, ev.Type)
	assert.True(t, ev.Key.Equals(NewKeyRune('P')), "editing must still get the capital letter")
	assert.True(t, ev.keyForBindings().Equals(NewKeyRune('p')))
}

func TestShiftLetterMatchesUppercaseBinding(t *testing.T) {
	ev := eventWithBindingKey(advancedKeyEvent(tcell.KeyRune, "P", tcell.ModShift))

	assert.True(t, ev.Key.Equals(NewKeyRune('P')), "Shift must be removed from text keys")
	assert.True(t, ev.keyForBindings().Equals(NewKeyRune('P')))
}

func TestShiftLetterWithCapsLockMatchesUppercaseBinding(t *testing.T) {
	ev := eventWithBindingKey(advancedKeyEvent(tcell.KeyRune, "p", tcell.ModShift))

	assert.True(t, ev.Key.Equals(NewKeyRune('p')))
	assert.True(t, ev.keyForBindings().Equals(NewKeyRune('P')))
}

func TestSideSpecificShiftIsPlainShift(t *testing.T) {
	ev := eventWithBindingKey(advancedKeyEvent(tcell.KeyRune, "P", tcell.ModLShift))

	assert.True(t, ev.Key.Equals(NewKeyRune('P')))
	assert.True(t, ev.keyForBindings().Equals(NewKeyRune('P')))
}

func TestShiftedSymbolHasNoBindingKey(t *testing.T) {
	ev := eventWithBindingKey(advancedKeyEvent(tcell.KeyRune, "!", tcell.ModShift))

	assert.True(t, ev.Key.Equals(NewKeyRune('!')))
	assert.False(t, ev.bindingKey.IsSet())
}

func TestCtrlLetterKeepsLegacyForm(t *testing.T) {
	ev := eventWithBindingKey(advancedKeyEvent(tcell.KeyRune, "a", tcell.ModLCtrl))

	assert.True(t, ev.Key.Equals(NewKeyStrMod("a", ModCtrl)))
	assert.False(t, ev.bindingKey.IsSet())
}

func TestAdvancedShiftTabIsBacktab(t *testing.T) {
	ev := gocuiEventFromTcellEvent(advancedKeyEvent(tcell.KeyBacktab, "", tcell.ModNone))

	assert.True(t, ev.Key.Equals(NewKeyName(KeyBacktab)))
}

func TestKeyReleaseAndLoneModifierAreIgnored(t *testing.T) {
	release := gocuiEventFromTcellEvent(tcell.NewEventKeyEx(tcell.KeyRune, "p", tcell.ModNone, false, 0, 1))
	shift := gocuiEventFromTcellEvent(advancedKeyEvent(tcell.KeyShift, "", tcell.ModLShift))
	capsLock := gocuiEventFromTcellEvent(advancedKeyEvent(tcell.KeyCapsLock, "", tcell.ModNone))

	assert.Equal(t, eventNone, release.Type)
	assert.Equal(t, eventNone, shift.Type)
	assert.Equal(t, eventNone, capsLock.Type)
}

func TestReportsShiftForText(t *testing.T) {
	assert.True(t, reportsShiftForText(tcell.KittyKeyboard))
	assert.True(t, reportsShiftForText(tcell.Win32Keyboard))
	assert.False(t, reportsShiftForText(tcell.XTermKeyboard))
	assert.False(t, reportsShiftForText(tcell.LegacyKeyboard))
}

func TestCapsLockRunsLowercaseBindingAndTypesCapital(t *testing.T) {
	g := newTestGui(t)
	v, _ := g.SetView("editor", 0, 0, 40, 10, 0)
	_, err := g.SetCurrentView("editor")
	assert.NoError(t, err)

	pulled, pushed := 0, 0
	g.SetKeybinding("editor", NewKeyRune('p'), func(*Gui, *View) error {
		pulled++
		return nil
	})
	g.SetKeybinding("editor", NewKeyRune('P'), func(*Gui, *View) error {
		pushed++
		return nil
	})

	capsLockP := eventWithBindingKey(advancedKeyEvent(tcell.KeyRune, "P", tcell.ModNone))
	assert.NoError(t, g.onKey(&capsLockP))
	shiftP := eventWithBindingKey(advancedKeyEvent(tcell.KeyRune, "P", tcell.ModShift))
	assert.NoError(t, g.onKey(&shiftP))

	assert.Equal(t, 1, pulled)
	assert.Equal(t, 1, pushed)

	g.DeleteAllKeybindings()
	v.Editable = true
	capsLockP = eventWithBindingKey(advancedKeyEvent(tcell.KeyRune, "P", tcell.ModNone))
	assert.NoError(t, g.onKey(&capsLockP))

	assert.Equal(t, "P", v.TextArea.GetContent())
}
