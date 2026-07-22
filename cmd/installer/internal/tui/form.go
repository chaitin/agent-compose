package tui

import (
	"strconv"
	"strings"

	"github.com/chaitin/agent-compose/cmd/installer/internal/core"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

type fieldID int

const (
	fieldInstallDir fieldID = iota
	fieldVersion
	fieldWithUI
	fieldPort
	fieldGuestPull
)

// formField is either a text input or an on/off toggle. Toggles cannot be
// expressed as a textinput.Model, and the port field has to render as disabled
// rather than disappear, so the form tracks fields instead of raw inputs.
type formField struct {
	id     fieldID
	toggle bool
	on     bool
	input  textinput.Model
}

func newTextField(id fieldID, value string) formField {
	input := textinput.New()
	input.SetValue(value)
	input.Prompt = ""
	input.CharLimit = 512
	return formField{id: id, input: input}
}

func newToggleField(id fieldID, on bool) formField {
	return formField{id: id, toggle: true, on: on}
}

func (m *model) buildFields() {
	m.fields = []formField{newTextField(fieldInstallDir, m.options.InstallDir)}
	if m.operation != core.OperationUninstall {
		m.fields = append(m.fields,
			newTextField(fieldVersion, m.options.Version),
			newToggleField(fieldWithUI, m.options.WithUI),
			newTextField(fieldPort, strconv.Itoa(m.options.Port)),
			newToggleField(fieldGuestPull, !m.options.SkipGuestPull),
		)
	}
	m.focus = 0
	m.focusFields()
}

func (m *model) field(id fieldID) *formField {
	for i := range m.fields {
		if m.fields[i].id == id {
			return &m.fields[i]
		}
	}
	return nil
}

// fieldDisabled reports fields that are visible but inert. The port only
// matters when the frontend publishes it, so leaving it greyed out shows the
// dependency without the form jumping around as the toggle flips.
func (m *model) fieldDisabled(index int) bool {
	if m.fields[index].id != fieldPort {
		return false
	}
	ui := m.field(fieldWithUI)
	return ui != nil && !ui.on
}

func (m *model) focusFields() {
	for i := range m.fields {
		if i == m.focus && !m.fields[i].toggle {
			m.fields[i].input.Focus()
			continue
		}
		m.fields[i].input.Blur()
	}
}

func (m *model) moveFocus(delta int) {
	for range m.fields {
		m.focus = (m.focus + delta + len(m.fields)) % len(m.fields)
		if !m.fieldDisabled(m.focus) {
			break
		}
	}
	m.focusFields()
}

func (m *model) toggleFocusedField() bool {
	if m.fields[m.focus].toggle {
		m.fields[m.focus].on = !m.fields[m.focus].on
		// Flipping the UI toggle can disable the field under the cursor only
		// when focus is already elsewhere, so refreshing focus is enough.
		m.focusFields()
		return true
	}
	return false
}

func (m *model) readFields() error {
	for i, field := range m.fields {
		switch field.id {
		case fieldInstallDir:
			m.options.InstallDir = strings.TrimSpace(field.input.Value())
		case fieldVersion:
			m.options.Version = strings.TrimSpace(field.input.Value())
		case fieldWithUI:
			m.options.WithUI = field.on
			m.options.WithUISet = true
		case fieldPort:
			// A disabled port publishes nothing. Reading it would validate a
			// value the operator cannot even edit, and marking it as set would
			// both override the port recorded in .env and trigger the warning
			// meant for a CLI --port that cannot take effect.
			if m.fieldDisabled(i) {
				continue
			}
			port, err := core.ParsePort(field.input.Value())
			if err != nil {
				return err
			}
			m.options.Port = port
			m.options.PortSet = true
		case fieldGuestPull:
			m.options.SkipGuestPull = !field.on
		}
	}
	if m.operation != core.OperationUninstall {
		m.options.InstallerPath = m.installerPath
	}
	return m.options.Validate(m.operation)
}

func (m *model) fieldLabel(id fieldID) string {
	switch id {
	case fieldInstallDir:
		return m.text("安装目录", "Install directory")
	case fieldVersion:
		return m.text("应用版本", "Application version")
	case fieldWithUI:
		return m.text("安装 Web UI", "Install web UI")
	case fieldPort:
		return m.text("Web UI 端口", "Web UI port")
	case fieldGuestPull:
		return m.text("预拉取 guest 镜像", "Pre-pull guest image")
	}
	return ""
}

func (m *model) renderForm(body *strings.Builder) {
	title, description := m.formHeading()
	body.WriteString(titleStyle.Render(title) + "\n")
	body.WriteString(mutedStyle.Render(description) + "\n\n")
	for i := range m.fields {
		m.renderFormField(body, i)
	}
	if m.err != nil {
		body.WriteString(warnStyle.Render("! "+m.err.Error()) + "\n")
	}
}

func (m *model) renderFormField(body *strings.Builder, index int) {
	field := m.fields[index]
	label := m.fieldLabel(field.id)
	focused := index == m.focus
	disabled := m.fieldDisabled(index)

	marker, labelStyle := mutedStyle.Render("○"), lipgloss.NewStyle()
	switch {
	case disabled:
		marker, labelStyle = mutedStyle.Render("·"), mutedStyle
		label += "  " + m.text("(未启用)", "(not enabled)")
	case focused:
		marker, labelStyle = selectedStyle.Render("●"), selectedStyle
	}
	body.WriteString(marker + " " + labelStyle.Render(label) + "\n")

	if field.toggle {
		body.WriteString("  " + m.renderToggle(field.on, focused) + "\n\n")
		return
	}
	boxStyle := fieldStyle
	if disabled {
		boxStyle = disabledField
	} else if focused {
		boxStyle = focusedField
	}
	width := m.fieldWidth()
	input := field.input
	input.Width = width - 2
	value := input.View()
	if disabled {
		value = mutedStyle.Render(field.input.Value())
	}
	body.WriteString(boxStyle.Width(width).Render(value) + "\n\n")
}

func (m *model) renderToggle(on, focused bool) string {
	label := m.text("否", "No")
	if on {
		label = m.text("是", "Yes")
	}
	rendered := "‹ " + label + " ›"
	if focused {
		return selectedStyle.Render(rendered) + mutedStyle.Render("  "+m.text("←→ / 空格 切换", "←→ / space toggles"))
	}
	return mutedStyle.Render(rendered)
}

func (m *model) fieldWidth() int {
	width := m.width - 8
	if width > 72 {
		width = 72
	}
	if width < 16 {
		width = 16
	}
	return width
}
