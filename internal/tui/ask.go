package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

type askPromptModel struct {
	textarea textarea.Model
	id       string
	title    string
	done     bool
	cancel   bool
}

func newAskPromptModel(id, title string) askPromptModel {
	ta := textarea.New()
	ta.Placeholder = "Ask a question about this session..."
	ta.Focus()
	ta.SetWidth(80)
	ta.SetHeight(6)
	return askPromptModel{textarea: ta, id: id, title: title}
}

func (m askPromptModel) Init() tea.Cmd { return textarea.Blink }

func (m askPromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+s", "ctrl+d":
			m.done = true
			return m, tea.Quit
		case "esc", "ctrl+c":
			m.cancel = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	return m, cmd
}

func (m askPromptModel) View() string {
	title := strings.TrimSpace(m.title)
	if title == "" {
		title = "Untitled session"
	}
	return fmt.Sprintf("Ask %s — %s\nCtrl+S or Ctrl+D to submit, Esc to cancel\n\n%s\n", m.id, title, m.textarea.View())
}

func PromptAskQuestion(stdout, stderr io.Writer, id, title string) (string, error) {
	m := newAskPromptModel(id, title)
	program := tea.NewProgram(m, tea.WithOutput(stdout))
	final, err := program.Run()
	if err != nil {
		return "", err
	}
	result, ok := final.(askPromptModel)
	if !ok || result.cancel {
		return "", nil
	}
	return normalizeAskQuestion(result.textarea.Value()), nil
}

func normalizeAskQuestion(s string) string { return strings.TrimSpace(s) }
