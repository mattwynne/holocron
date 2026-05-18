package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattwynne/holocron/internal/index"
)

type fakeStore struct {
	searches []string
	recent   []index.SearchResult
	results  []index.SearchResult
}

func (f *fakeStore) Search(query string, limit int) ([]index.SearchResult, error) {
	f.searches = append(f.searches, query)
	return f.results, nil
}

func (f *fakeStore) Recent(limit int) ([]index.SearchResult, error) {
	return f.recent, nil
}

func TestModelEnterChoosesSelectedResult(t *testing.T) {
	m := NewModel(&fakeStore{}, "")
	m.results = []index.SearchResult{{ID: "one"}, {ID: "two"}}
	m.selected = 1
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	if got.choice != "two" || got.cancel {
		t.Fatalf("choice=%q cancel=%v", got.choice, got.cancel)
	}
}

func TestModelEscapeCancels(t *testing.T) {
	m := NewModel(&fakeStore{}, "")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := updated.(Model)
	if !got.cancel || got.choice != "" {
		t.Fatalf("choice=%q cancel=%v", got.choice, got.cancel)
	}
}

func TestModelMovementIsBounded(t *testing.T) {
	m := NewModel(&fakeStore{}, "")
	m.results = []index.SearchResult{{ID: "one"}, {ID: "two"}}
	m.selected = 0
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(Model)
	if m.selected != 0 {
		t.Fatalf("selected after up = %d", m.selected)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.selected != 1 {
		t.Fatalf("selected after downs = %d", m.selected)
	}
}

func TestTypingUpdatesQueryAndDebounceSearchesLatest(t *testing.T) {
	store := &fakeStore{results: []index.SearchResult{{ID: "one"}}}
	m := NewModel(store, "")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	m = updated.(Model)
	if m.query != "h" || !m.loading {
		t.Fatalf("query=%q loading=%v", m.query, m.loading)
	}
	updated, cmd = m.Update(debounceMsg{query: "h"})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected search command")
	}
	msg := cmd().(searchMsg)
	if msg.query != "h" || len(store.searches) != 1 || store.searches[0] != "h" {
		t.Fatalf("msg=%#v searches=%#v", msg, store.searches)
	}
}

func TestCtrlUClearsQuery(t *testing.T) {
	m := NewModel(&fakeStore{}, "hello")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	got := updated.(Model)
	if got.query != "" || !got.loading {
		t.Fatalf("query=%q loading=%v", got.query, got.loading)
	}
}

func TestRenderRowShowsFolderIDAndSizeBar(t *testing.T) {
	m := NewModel(&fakeStore{}, "")
	m.results = []index.SearchResult{
		{ID: "pi:small", Tool: "pi", Title: "Fix auth", Folder: "project", CWD: "/tmp/project", FileSizeBytes: 25},
		{ID: "pi:large", Tool: "pi", Title: "Big session", Folder: "other", CWD: "/tmp/other", FileSizeBytes: 100},
	}
	row := m.renderRow(0)
	for _, want := range []string{"project", "pi:small", "███░░░░░░░"} {
		if !strings.Contains(row, want) {
			t.Fatalf("row missing %q: %q", want, row)
		}
	}
	for _, unwanted := range []string{"msgs", "25 B"} {
		if strings.Contains(row, unwanted) {
			t.Fatalf("row contains %q: %q", unwanted, row)
		}
	}
}
