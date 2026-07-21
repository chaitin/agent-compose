package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/cmd/installer/internal/core"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type screen int

const (
	screenLanguage screen = iota
	screenAction
	screenForm
	screenPurge
	screenConfirm
	screenRunning
	screenDone
)

type language string

const (
	chinese language = "中文"
	english language = "English"
)

type operationResult struct {
	result core.Result
	err    error
}

type eventMessage core.Event

type model struct {
	service       core.Service
	ctx           context.Context
	cancel        context.CancelFunc
	options       core.Options
	installerPath string
	program       *tea.Program
	screen        screen
	language      language
	cursor        int
	operation     core.Operation
	fields        []formField
	focus         int
	spinner       spinner.Model
	cancelling    bool
	events        []string
	result        core.Result
	err           error
	width         int
	height        int
}

var (
	brandStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	taglineStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	warnStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	fieldStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	focusedField  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("42")).Padding(0, 1)
	disabledField = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("238")).Padding(0, 1)
)

const productBanner = `    _                    _          ____
   / \   __ _  ___ _ __| |_       / ___|___  _ __ ___  _ __   ___  ___  ___
  / _ \ / _' |/ _ \ '_ \ __|_____| |   / _ \| '_ ' _ \| '_ \ / _ \/ __|/ _ \
 / ___ \ (_| |  __/ | | | ||_____| |__| (_) | | | | | | |_) | (_) \__ \  __/
/_/   \_\__, |\___|_| |_|\__|     \____\___/|_| |_| |_| .__/ \___/|___/\___|
        |___/                                          |_|`

const productTagline = ":: DECLARATIVE AGENT RUNTIME :: INSTALLER ::"

func Run(service core.Service, defaults core.Options, installerPath string) error {
	m := newModel(service, defaults, installerPath)
	defer m.cancel()
	switch m.service.Runner.(type) {
	case nil, core.ExecRunner:
		m.service.Runner = core.ExecRunner{Output: newCommandOutputWriter(func(line string) {
			if m.program != nil {
				m.program.Send(commandOutputMessage(line))
			}
		})}
	}
	program := tea.NewProgram(m, tea.WithAltScreen())
	m.program = program
	final, err := program.Run()
	if err != nil {
		return err
	}
	return final.(*model).err
}

func newModel(service core.Service, defaults core.Options, installerPath string) *model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	ctx, cancel := context.WithCancel(context.Background())
	return &model{service: service, ctx: ctx, cancel: cancel, options: defaults, installerPath: installerPath, screen: screenLanguage, language: chinese, spinner: sp, width: 100, height: 40}
}

func (m *model) Init() tea.Cmd { return nil }

func (m *model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		return m, nil
	case eventMessage:
		m.appendEvent(core.Event(msg).Message)
		return m, nil
	case commandOutputMessage:
		m.appendEvent(string(msg))
		return m, nil
	case operationResult:
		m.result, m.err, m.screen = msg.result, msg.err, screenDone
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" && m.screen == screenRunning {
			// Repeated presses must not stack identical lines; cancellation is
			// already under way and the log is the only feedback available.
			if !m.cancelling {
				m.cancelling = true
				m.appendEvent(m.text("正在取消并回滚...", "Cancelling and rolling back..."))
			}
			m.cancel()
			return m, nil
		}
		if msg.String() == "ctrl+c" || (msg.String() == "q" && m.screen != screenForm) {
			m.cancel()
			return m, tea.Quit
		}
		return m.updateKey(msg)
	}
	return m, nil
}

func (m *model) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenLanguage:
		return m.updateMenu(key, 2, func(choice int) {
			if choice == 1 {
				m.language = english
			}
			m.cursor, m.screen = 0, screenAction
		})
	case screenAction:
		return m.updateMenu(key, 4, func(choice int) {
			if choice == 3 {
				m.err = nil
				m.screen = screenDone
				return
			}
			m.operation = []core.Operation{core.OperationInstall, core.OperationUpgrade, core.OperationUninstall}[choice]
			m.buildFields()
			m.cursor, m.screen = 0, screenForm
		})
	case screenForm:
		if key.String() == "tab" || key.String() == "down" || key.String() == "shift+tab" || key.String() == "up" {
			delta := 1
			if key.String() == "shift+tab" || key.String() == "up" {
				delta = -1
			}
			m.moveFocus(delta)
			return m, nil
		}
		if key.String() == "left" || key.String() == "right" || key.String() == " " {
			if m.toggleFocusedField() {
				return m, nil
			}
		}
		if key.String() == "enter" {
			if err := m.readFields(); err != nil {
				m.err = err
				return m, nil
			}
			m.err = nil
			if m.operation == core.OperationUninstall {
				m.screen = screenPurge
			} else {
				m.screen = screenConfirm
			}
			return m, nil
		}
		if m.fields[m.focus].toggle {
			return m, nil
		}
		var cmd tea.Cmd
		m.fields[m.focus].input, cmd = m.fields[m.focus].input.Update(key)
		return m, cmd
	case screenPurge:
		return m.updateMenu(key, 2, func(choice int) { m.options.Purge = choice == 1; m.cursor, m.screen = 0, screenConfirm })
	case screenConfirm:
		return m.updateMenu(key, 2, func(choice int) {
			if choice == 1 {
				m.screen = screenAction
				m.cursor = 0
				return
			}
			m.screen = screenRunning
		})
	case screenRunning:
		return m, nil
	case screenDone:
		if key.String() == "enter" || key.String() == "esc" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *model) updateMenu(key tea.KeyMsg, count int, selectFn func(int)) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "up", "k":
		m.cursor = (m.cursor - 1 + count) % count
	case "down", "j":
		m.cursor = (m.cursor + 1) % count
	case "enter":
		selectFn(m.cursor)
		if m.screen == screenRunning {
			return m, tea.Batch(m.spinner.Tick, m.runOperation())
		}
	}
	return m, nil
}

func (m *model) runOperation() tea.Cmd {
	return func() tea.Msg {
		service := m.service
		service.Reporter = core.ReporterFunc(func(event core.Event) {
			if m.program != nil {
				m.program.Send(eventMessage(event))
			}
		})
		result, err := service.Apply(m.ctx, m.operation, m.options)
		return operationResult{result: result, err: err}
	}
}

func (m *model) View() string {
	var body strings.Builder
	m.renderBrand(&body)
	switch m.screen {
	case screenLanguage:
		m.renderMenu(&body, "选择语言 / Choose language", []string{string(chinese), string(english)})
	case screenAction:
		m.renderMenu(&body, m.text("请选择操作", "Choose an action"), []string{m.text("安装", "Install"), m.text("升级", "Upgrade"), m.text("卸载", "Uninstall"), m.text("退出", "Quit")})
	case screenForm:
		m.renderForm(&body)
	case screenPurge:
		m.renderMenu(&body, m.text("卸载后如何处理数据？", "What should happen to data?"), []string{m.text("保留配置和数据", "Preserve configuration and data"), m.text("永久删除配置和数据", "Permanently delete configuration and data")})
	case screenConfirm:
		m.renderConfirm(&body)
	case screenRunning:
		body.WriteString(m.spinner.View() + " " + m.text("正在执行...", "Working...") + "\n\n")
		for _, event := range m.visibleEvents() {
			body.WriteString("  " + event + "\n")
		}
	case screenDone:
		m.renderDone(&body)
	}
	body.WriteString("\n")
	body.WriteString(m.renderKeyHelp())
	return body.String()
}

func (m *model) renderKeyHelp() string {
	var help string
	switch m.screen {
	case screenForm:
		help = m.text("Tab / ↑↓ 切换字段  ·  Enter 继续  ·  Ctrl+C 退出", "Tab / ↑↓ move  ·  Enter continue  ·  Ctrl+C quit")
	case screenRunning:
		help = m.text("Ctrl+C 取消并回滚", "Ctrl+C cancel and roll back")
	case screenDone:
		help = m.text("Enter / Esc 退出", "Enter / Esc quit")
	default:
		help = m.text("↑↓ 选择  ·  Enter 确认  ·  Ctrl+C 退出", "↑↓ select  ·  Enter confirm  ·  Ctrl+C quit")
	}
	return mutedStyle.Render(help)
}

func (m *model) renderBrand(body *strings.Builder) {
	if m.width >= 80 {
		body.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, brandStyle.Render(productBanner)))
		body.WriteString("\n")
		body.WriteString(taglineStyle.Width(m.width).Align(lipgloss.Center).Render(productTagline))
		body.WriteString("\n\n")
		return
	}
	body.WriteString(titleStyle.Render("agent-compose :: installer"))
	body.WriteString("\n\n")
}

func (m *model) renderMenu(body *strings.Builder, title string, choices []string) {
	body.WriteString(title + "\n\n")
	for i, choice := range choices {
		prefix := "  "
		if i == m.cursor {
			prefix = "› "
			choice = selectedStyle.Render(choice)
		}
		body.WriteString(prefix + choice + "\n")
	}
}

func (m *model) formHeading() (string, string) {
	switch m.operation {
	case core.OperationUpgrade:
		return m.text("配置升级", "Configure upgrade"), m.text("确认安装位置和要升级到的版本", "Review the installation and target version")
	case core.OperationUninstall:
		return m.text("选择安装位置", "Select installation"), m.text("指定要卸载的 agent-compose 目录", "Choose the agent-compose directory to uninstall")
	default:
		return m.text("配置安装", "Configure installation"), m.text("确认安装位置、发布版本和 Web UI", "Review the location, release, and web UI")
	}
}

func (m *model) renderConfirm(body *strings.Builder) {
	body.WriteString(m.text("执行计划", "Plan") + "\n\n")
	fmt.Fprintf(body, "  %s: %s\n  %s: %s\n", m.text("操作", "Operation"), m.operation, m.text("目录", "Directory"), m.options.InstallDir)
	if m.operation == core.OperationUninstall {
		fmt.Fprintf(body, "  %s: %t\n", m.text("删除数据", "Purge data"), m.options.Purge)
	} else {
		fmt.Fprintf(body, "  %s: %s\n", m.text("版本", "Version"), m.options.Version)
		fmt.Fprintf(body, "  %s: %s\n", m.text("安装 Web UI", "Install web UI"), m.yesNo(m.options.WithUI))
		if m.options.WithUI {
			fmt.Fprintf(body, "  %s: %d\n", m.text("Web UI 端口", "Web UI port"), m.options.Port)
		}
	}
	body.WriteString("\n")
	m.renderMenu(body, m.text("确认继续？", "Continue?"), []string{m.text("继续", "Continue"), m.text("返回", "Back")})
}

func (m *model) renderDone(body *strings.Builder) {
	if m.err != nil {
		body.WriteString(warnStyle.Render(m.text("操作失败：", "Failed: ")+m.err.Error()) + "\n")
		return
	}
	body.WriteString(selectedStyle.Render(m.text("操作完成", "Completed")) + "\n")
	if m.result.URL != "" {
		body.WriteString("URL: " + m.result.URL + "\n")
	}
	if m.result.GeneratedPassword != "" {
		body.WriteString("Username: " + m.result.Username + "\nPassword: " + m.result.GeneratedPassword + "\n")
	}
	if m.operation != core.OperationUninstall && !m.result.WithUI() {
		body.WriteString(mutedStyle.Render(m.text("Web UI 未安装。启用：", "Web UI is not installed. Enable it with:")) + "\n")
		body.WriteString(mutedStyle.Render("  cd "+m.result.InstallDir) + "\n")
		body.WriteString(mutedStyle.Render("  docker compose --profile with-ui up -d") + "\n")
	}
	if len(m.result.RetainedFiles) > 0 {
		body.WriteString(m.text("保留的未知文件：", "Unknown files retained: ") + strings.Join(m.result.RetainedFiles, ", ") + "\n")
	}
}

func (m *model) appendEvent(message string) {
	if message == "" {
		return
	}
	m.events = append(m.events, message)
	if len(m.events) > 50 {
		m.events = append([]string(nil), m.events[len(m.events)-50:]...)
	}
}

func (m *model) visibleEvents() []string {
	limit := m.height - 14
	if m.width < 80 {
		limit = m.height - 7
	}
	if limit < 3 {
		limit = 3
	}
	if limit > 12 {
		limit = 12
	}
	if len(m.events) <= limit {
		return m.events
	}
	return m.events[len(m.events)-limit:]
}

func (m *model) yesNo(value bool) string {
	if value {
		return m.text("是", "Yes")
	}
	return m.text("否", "No")
}

func (m *model) text(zh, en string) string {
	if m.language == english {
		return en
	}
	return zh
}
