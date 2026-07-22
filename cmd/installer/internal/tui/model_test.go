package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/cmd/installer/internal/core"
	tea "github.com/charmbracelet/bubbletea"
)

func TestModelSelectsLanguageAndInstallFlow(t *testing.T) {
	m := newModel(core.Service{}, core.DefaultOptions(), "/tmp/installer")
	if view := m.View(); !strings.Contains(view, "DECLARATIVE AGENT RUNTIME") || !strings.Contains(view, "|___/") {
		t.Fatalf("wide TUI is missing product banner:\n%s", view)
	}
	press(t, m, "down")
	press(t, m, "enter")
	if m.language != english || m.screen != screenAction {
		t.Fatalf("language screen = %q, %d", m.language, m.screen)
	}
	press(t, m, "enter")
	if m.operation != core.OperationInstall || m.screen != screenForm || len(m.fields) != 5 {
		t.Fatalf("install form = %q, %d, %d fields", m.operation, m.screen, len(m.fields))
	}
	form := m.View()
	for _, expected := range []string{"Configure installation", "Install directory", "Application version", "Install web UI", "Web UI port", "Pre-pull guest image", "╭", "Tab / ↑↓ move"} {
		if !strings.Contains(form, expected) {
			t.Fatalf("install form missing %q:\n%s", expected, form)
		}
	}
	if strings.Contains(form, "Image prefix") {
		t.Fatalf("advanced image prefix rendered in TUI:\n%s", form)
	}
	m.fields[0].input.SetValue("relative")
	press(t, m, "enter")
	if m.err == nil || !strings.Contains(m.View(), "absolute") {
		t.Fatalf("expected path validation in view: %v\n%s", m.err, m.View())
	}
	m.fields[0].input.SetValue("/opt/agent-compose")
	press(t, m, "enter")
	if m.screen != screenConfirm {
		t.Fatalf("screen = %d, want confirmation", m.screen)
	}
	confirmation := m.View()
	for _, expected := range []string{"/opt/agent-compose", "latest", "Install web UI: No", "Pre-pull guest image: Yes"} {
		if !strings.Contains(confirmation, expected) {
			t.Fatalf("confirmation missing %q:\n%s", expected, confirmation)
		}
	}
	if strings.Contains(confirmation, "Web UI port") {
		t.Fatalf("confirmation offered a port without the web UI:\n%s", confirmation)
	}
}

func TestModelPortFollowsWebUIToggle(t *testing.T) {
	m := installForm(t)
	port := indexOfField(t, m, fieldPort)

	if !m.fieldDisabled(port) {
		t.Fatal("port is editable while the web UI is disabled")
	}
	form := m.View()
	if !strings.Contains(form, "(not enabled)") {
		t.Fatalf("disabled port is not marked in the form:\n%s", form)
	}

	// Tab from the UI toggle must land past the disabled port.
	m.focus = indexOfField(t, m, fieldWithUI)
	m.moveFocus(1)
	if m.fields[m.focus].id != fieldGuestPull {
		t.Fatalf("focus stopped on field %d, want the guest toggle", m.fields[m.focus].id)
	}

	m.focus = indexOfField(t, m, fieldWithUI)
	press(t, m, "right")
	if m.fieldDisabled(port) {
		t.Fatal("port stayed disabled after enabling the web UI")
	}
	m.moveFocus(1)
	if m.fields[m.focus].id != fieldPort {
		t.Fatalf("focus skipped the re-enabled port, landed on %d", m.fields[m.focus].id)
	}

	press(t, m, "enter")
	if !m.options.WithUI || !m.options.WithUISet {
		t.Fatalf("WithUI = %t, WithUISet = %t", m.options.WithUI, m.options.WithUISet)
	}
	if confirmation := m.View(); !strings.Contains(confirmation, "Web UI port") {
		t.Fatalf("confirmation hid the port with the web UI enabled:\n%s", confirmation)
	}
}

func TestModelGuestPullToggleSetsSkip(t *testing.T) {
	m := installForm(t)
	if m.options.SkipGuestPull {
		t.Fatal("guest pull is skipped by default")
	}
	m.focus = indexOfField(t, m, fieldGuestPull)
	press(t, m, " ")
	press(t, m, "enter")
	if !m.options.SkipGuestPull {
		t.Fatal("toggling the guest field did not set SkipGuestPull")
	}
}

// A greyed-out port is not a choice the operator made, so it must not be
// validated, must not override the port already recorded in .env, and must not
// trip the warning meant for a CLI --port that cannot take effect.
func TestModelDisabledPortIsNotTreatedAsAChoice(t *testing.T) {
	m := installForm(t)
	m.field(fieldPort).input.SetValue("not-a-port")

	press(t, m, "enter")
	if m.err != nil {
		t.Fatalf("disabled port was validated: %v", m.err)
	}
	if m.screen != screenConfirm {
		t.Fatalf("screen = %d, want confirmation", m.screen)
	}
	if m.options.PortSet {
		t.Fatal("disabled port was recorded as an explicit choice")
	}
}

func TestModelEnabledPortIsValidated(t *testing.T) {
	m := installForm(t)
	m.field(fieldWithUI).on = true
	m.field(fieldPort).input.SetValue("not-a-port")

	press(t, m, "enter")
	if m.err == nil || m.screen == screenConfirm {
		t.Fatalf("enabled port skipped validation: err=%v screen=%d", m.err, m.screen)
	}

	m.field(fieldPort).input.SetValue("18080")
	press(t, m, "enter")
	if !m.options.PortSet || m.options.Port != 18080 {
		t.Fatalf("PortSet=%t Port=%d", m.options.PortSet, m.options.Port)
	}
}

func installForm(t *testing.T) *model {
	t.Helper()
	m := newModel(core.Service{}, core.DefaultOptions(), "/tmp/installer")
	press(t, m, "down")
	press(t, m, "enter")
	press(t, m, "enter")
	if m.screen != screenForm {
		t.Fatalf("screen = %d, want the install form", m.screen)
	}
	return m
}

func indexOfField(t *testing.T, m *model, id fieldID) int {
	t.Helper()
	for i := range m.fields {
		if m.fields[i].id == id {
			return i
		}
	}
	t.Fatalf("field %d is missing from the form", id)
	return -1
}

func TestModelUsesCompactBrandOnNarrowTerminal(t *testing.T) {
	m := newModel(core.Service{}, core.DefaultOptions(), "/tmp/installer")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	m = updated.(*model)
	view := m.View()
	if !strings.Contains(view, "agent-compose :: installer") {
		t.Fatalf("compact brand missing:\n%s", view)
	}
	if strings.Contains(view, "DECLARATIVE AGENT RUNTIME") {
		t.Fatalf("wide tagline rendered on narrow terminal:\n%s", view)
	}
}

func TestModelUninstallPurgeChoice(t *testing.T) {
	m := newModel(core.Service{}, core.DefaultOptions(), "/tmp/installer")
	press(t, m, "enter")
	press(t, m, "down")
	press(t, m, "down")
	press(t, m, "enter")
	if m.operation != core.OperationUninstall || len(m.fields) != 1 {
		t.Fatalf("uninstall form = %q, %d fields", m.operation, len(m.fields))
	}
	press(t, m, "enter")
	if m.screen != screenPurge {
		t.Fatalf("screen = %d, want purge", m.screen)
	}
	press(t, m, "down")
	press(t, m, "enter")
	if !m.options.Purge || m.screen != screenConfirm {
		t.Fatalf("purge = %t, screen = %d", m.options.Purge, m.screen)
	}
}

func TestModelRendersProgressAndResults(t *testing.T) {
	m := newModel(core.Service{}, core.DefaultOptions(), "/tmp/installer")
	t.Cleanup(m.cancel)
	m.screen = screenRunning
	updated, _ := m.Update(eventMessage(core.Event{Message: "Pulling images"}))
	m = updated.(*model)
	if !strings.Contains(m.View(), "Pulling images") {
		t.Fatalf("progress missing:\n%s", m.View())
	}
	updated, _ = m.Update(commandOutputMessage("layer downloaded"))
	m = updated.(*model)
	if !strings.Contains(m.View(), "layer downloaded") {
		t.Fatalf("command output missing:\n%s", m.View())
	}
	updated, _ = m.Update(operationResult{result: core.Result{URL: "http://localhost:80", Username: "admin", GeneratedPassword: "secret"}})
	m = updated.(*model)
	if !strings.Contains(m.View(), "http://localhost:80") || !strings.Contains(m.View(), "secret") {
		t.Fatalf("result missing:\n%s", m.View())
	}
}

func TestModelBoundsVisibleCommandOutput(t *testing.T) {
	m := newModel(core.Service{}, core.DefaultOptions(), "/tmp/installer")
	m.screen = screenRunning
	m.width, m.height = 120, 20
	for i := range 20 {
		m.appendEvent(fmt.Sprintf("line-%02d", i))
	}
	view := m.View()
	if strings.Contains(view, "line-00") || !strings.Contains(view, "line-19") {
		t.Fatalf("visible output was not bounded to its tail:\n%s", view)
	}
}

func TestModelCancelsRunningOperationBeforeExit(t *testing.T) {
	m := newModel(core.Service{}, core.DefaultOptions(), "/tmp/installer")
	m.screen = screenRunning
	updated, command := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if updated != m || command != nil {
		t.Fatal("running cancellation unexpectedly quit the TUI")
	}
	select {
	case <-m.ctx.Done():
	default:
		t.Fatal("running operation context was not cancelled")
	}
	if !strings.Contains(m.View(), "回滚") {
		t.Fatalf("cancellation state missing from view:\n%s", m.View())
	}

	// Cancellation is not instant, so impatient repeats must not stack lines.
	for range 5 {
		m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	}
	if count := strings.Count(m.View(), "正在取消并回滚"); count != 1 {
		t.Fatalf("cancellation notice rendered %d times:\n%s", count, m.View())
	}
}

func TestModelIgnoresKeysWhileOperationIsRunning(t *testing.T) {
	m := newModel(core.Service{}, core.DefaultOptions(), "/tmp/installer")
	m.screen = screenConfirm
	m.cursor = 0

	updated, command := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated != m || command == nil || m.screen != screenRunning {
		t.Fatalf("confirmation did not start operation: screen=%d command=%v", m.screen, command)
	}

	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyUp},
		{Type: tea.KeyDown},
		{Type: tea.KeyRunes, Runes: []rune("x")},
	} {
		updated, command = m.Update(key)
		if updated != m || command != nil {
			t.Fatalf("running key %q started another command", key.String())
		}
	}
}

func press(t *testing.T, m *model, key string) {
	t.Helper()
	keyType := tea.KeyRunes
	runes := []rune(key)
	switch key {
	case "enter":
		keyType, runes = tea.KeyEnter, nil
	case "up":
		keyType, runes = tea.KeyUp, nil
	case "down":
		keyType, runes = tea.KeyDown, nil
	case "left":
		keyType, runes = tea.KeyLeft, nil
	case "right":
		keyType, runes = tea.KeyRight, nil
	case " ":
		keyType, runes = tea.KeyRunes, []rune{' '}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: keyType, Runes: runes})
	if updated != m {
		t.Fatal("model pointer changed unexpectedly")
	}
}
