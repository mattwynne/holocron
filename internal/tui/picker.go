package tui

import (
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattwynne/holocron/internal/index"
)

type Store interface {
	Search(query string, limit int) ([]index.SearchResult, error)
	Recent(limit int) ([]index.SearchResult, error)
}

type Result struct {
	ID string
	OK bool
}

type searchMsg struct {
	query   string
	results []index.SearchResult
	err     error
}

type debounceMsg struct{ query string }

type Model struct {
	store    Store
	query    string
	results  []index.SearchResult
	selected int
	loading  bool
	err      error
	choice   string
	cancel   bool
	width    int
	height   int
}

func Pick(store Store, initialQuery string, stdout, stderr io.Writer) (Result, error) {
	m := NewModel(store, initialQuery)
	program := tea.NewProgram(m, tea.WithOutput(stdout), tea.WithAltScreen())
	finalModel, err := program.Run()
	if err != nil {
		return Result{}, err
	}
	m, ok := finalModel.(Model)
	if !ok {
		return Result{}, fmt.Errorf("unexpected picker model %T", finalModel)
	}
	if m.cancel || m.choice == "" {
		return Result{OK: false}, nil
	}
	return Result{ID: m.choice, OK: true}, nil
}

func NewModel(store Store, initialQuery string) Model {
	return Model{store: store, query: initialQuery, loading: true, width: 80, height: 24}
}

func (m Model) Init() tea.Cmd {
	return m.runSearch(m.query)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case searchMsg:
		if msg.query != m.query {
			return m, nil
		}
		m.loading = false
		m.err = msg.err
		m.results = msg.results
		if m.selected >= len(m.results) {
			m.selected = len(m.results) - 1
		}
		if m.selected < 0 {
			m.selected = 0
		}
		return m, nil
	case debounceMsg:
		if msg.query != m.query {
			return m, nil
		}
		m.loading = true
		return m, m.runSearch(m.query)
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.cancel = true
			return m, tea.Quit
		case tea.KeyEnter:
			if len(m.results) > 0 && m.selected >= 0 && m.selected < len(m.results) {
				m.choice = m.results[m.selected].ID
				return m, tea.Quit
			}
			return m, nil
		case tea.KeyUp, tea.KeyCtrlP:
			if m.selected > 0 {
				m.selected--
			}
			return m, nil
		case tea.KeyDown, tea.KeyCtrlN:
			if m.selected < len(m.results)-1 {
				m.selected++
			}
			return m, nil
		case tea.KeyPgUp:
			m.selected -= pageSize(m.height)
			if m.selected < 0 {
				m.selected = 0
			}
			return m, nil
		case tea.KeyPgDown:
			m.selected += pageSize(m.height)
			if m.selected >= len(m.results) {
				m.selected = len(m.results) - 1
			}
			if m.selected < 0 {
				m.selected = 0
			}
			return m, nil
		case tea.KeyCtrlU:
			if m.query == "" {
				return m, nil
			}
			m.query = ""
			m.selected = 0
			m.loading = true
			return m, debounce(m.query)
		case tea.KeyBackspace, tea.KeyDelete:
			if m.query == "" {
				return m, nil
			}
			r := []rune(m.query)
			m.query = string(r[:len(r)-1])
			m.selected = 0
			m.loading = true
			return m, debounce(m.query)
		case tea.KeySpace:
			m.query += " "
			m.selected = 0
			m.loading = true
			return m, debounce(m.query)
		case tea.KeyRunes:
			m.query += string(msg.Runes)
			m.selected = 0
			m.loading = true
			return m, debounce(m.query)
		}
	}
	return m, nil
}

func (m Model) View() string {
	var b strings.Builder
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))

	fmt.Fprintf(&b, "%s %s\n", titleStyle.Render("Holocron"), muted.Render("type to search • enter open • esc quit"))
	fmt.Fprintf(&b, "> %s\n\n", m.query)

	if m.err != nil {
		fmt.Fprintf(&b, "%s\n", errorStyle.Render(m.err.Error()))
	} else if m.loading {
		fmt.Fprintf(&b, "%s\n", muted.Render("loading..."))
	} else if len(m.results) == 0 {
		fmt.Fprintf(&b, "%s\n", muted.Render("no sessions found"))
	} else {
		visible := pageSize(m.height)
		start := 0
		if m.selected >= visible {
			start = m.selected - visible + 1
		}
		end := start + visible
		if end > len(m.results) {
			end = len(m.results)
		}
		for i := start; i < end; i++ {
			b.WriteString(m.renderRow(i))
			b.WriteByte('\n')
		}
	}

	fmt.Fprintf(&b, "\n%s", muted.Render("↑/↓ move • ctrl-u clear • pgup/pgdn page"))
	return b.String()
}

func (m Model) renderRow(i int) string {
	r := m.results[i]
	prefix := "  "
	style := lipgloss.NewStyle()
	if i == m.selected {
		prefix = "› "
		style = style.Foreground(lipgloss.Color("15")).Background(lipgloss.Color("4"))
	}
	date := shortUpdated(r.UpdatedAt)
	bar := sizeBar(r.FileSizeBytes, m.maxFileSize(), 10)
	detail := r.CWD
	if r.Folder != "" && r.Folder != r.CWD {
		detail = fmt.Sprintf("%s  %s", r.Folder, r.CWD)
	}
	line1 := truncate(fmt.Sprintf("%s%s  %s", prefix, r.Title, r.ID), m.width)
	line2 := truncate(fmt.Sprintf("  %-6s %s  %s  %s", r.Tool, date, bar, detail), m.width)
	return style.Render(line1) + "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(line2)
}

func (m Model) maxFileSize() int64 {
	var max int64
	for _, r := range m.results {
		if r.FileSizeBytes > max {
			max = r.FileSizeBytes
		}
	}
	return max
}

func sizeBar(value, max int64, width int) string {
	if width <= 0 {
		return ""
	}
	filled := 0
	if value > 0 && max > 0 {
		filled = int((value*int64(width) + max - 1) / max)
		if filled < 1 {
			filled = 1
		}
		if filled > width {
			filled = width
		}
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

func (m Model) runSearch(query string) tea.Cmd {
	return func() tea.Msg {
		var (
			results []index.SearchResult
			err     error
		)
		if strings.TrimSpace(query) == "" {
			results, err = m.store.Recent(100)
		} else {
			results, err = m.store.Search(query, 100)
		}
		return searchMsg{query: query, results: results, err: err}
	}
}

func debounce(query string) tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg {
		return debounceMsg{query: query}
	})
}

func pageSize(height int) int {
	n := height - 6
	if n < 3 {
		return 3
	}
	return n / 2
}

func truncate(s string, width int) string {
	if width <= 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}

func shortUpdated(value string) string {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return t.Local().Format("2006-01-02 15:04")
}
