package app

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/effects"
	"github.com/sageox/ox/internal/dashboard/overlays"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testOverlay struct {
	id       overlays.OverlayID
	updated  overlays.Overlay
	cmd      tea.Cmd
	consumed bool
}

func (o testOverlay) ID() overlays.OverlayID { return o.id }

func (o testOverlay) Update(tea.Msg) (overlays.Overlay, tea.Cmd, bool) {
	return o.updated, o.cmd, o.consumed
}

func (o testOverlay) View(int, int) string { return "test overlay" }

func TestReduceEffectsRejectsStaleGeneration(t *testing.T) {
	original := []session.SessionInfo{{Filename: "current.jsonl"}}
	m := Model{effectGen: 4}
	m.store.Sessions = original

	stale, cmd := m.reduceEffects(effects.SessionsLoadedMsg{
		Gen:      3,
		Sessions: []session.SessionInfo{{Filename: "stale.jsonl"}},
	})

	assert.Nil(t, cmd)
	assert.Equal(t, original, stale.store.Sessions)
	assert.Zero(t, stale.store.SessionsLoadedAt, "discarded responses must not claim freshness")
}

func TestReduceEffectsAppliesCurrentGenerationIncludingErrors(t *testing.T) {
	wantErr := errors.New("session load failed")
	want := []session.SessionInfo{{Filename: "current.jsonl"}}
	m := Model{effectGen: 4}

	got, cmd := m.reduceEffects(effects.SessionsLoadedMsg{Gen: 4, Sessions: want, Err: wantErr})

	assert.Nil(t, cmd)
	assert.Equal(t, want, got.store.Sessions)
	assert.ErrorIs(t, got.store.SessionsErr, wantErr)
	assert.Zero(t, got.store.SessionsLoadedAt, "failed responses must not claim freshness")
}

func TestReduceEffectsMaintainsMurmurSearchInvariants(t *testing.T) {
	m := Model{}
	m.store.TimelineCursorPos = 5

	m, _ = m.reduceEffects(MurmurSearchOpenMsg{})
	assert.True(t, m.store.MurmurQueryOpen)

	m, _ = m.reduceEffects(MurmurSearchQueryMsg{Query: "sync"})
	assert.Equal(t, "sync", m.store.MurmurQuery)
	assert.Zero(t, m.store.TimelineCursorPos)

	m, _ = m.reduceEffects(MurmurSearchCloseMsg{})
	assert.False(t, m.store.MurmurQueryOpen)
	assert.Empty(t, m.store.MurmurQuery)
}

func TestUpdateDoesNotPropagateOverlayConsumedMessages(t *testing.T) {
	original := &domain.InspectorTarget{Kind: domain.TargetSession}
	replacement := &domain.InspectorTarget{Kind: domain.TargetCodeDB}
	m := Model{listLens: &[int(sectionCount)]int{}}
	m.store.Selected = original
	m.overlays.Push(testOverlay{consumed: true})

	updated, cmd := m.Update(SelectionChangedMsg{Target: replacement})
	got := updated.(Model)

	assert.Nil(t, cmd)
	assert.Same(t, original, got.store.Selected, "consumed message must not reach effect reducers")
	assert.True(t, got.overlays.IsEmpty(), "nil updated overlay closes the top overlay")
}

func TestUpdatePropagatesMessagesDeclinedByOverlay(t *testing.T) {
	target := &domain.InspectorTarget{Kind: domain.TargetCodeDB}
	overlay := testOverlay{consumed: false}
	m := Model{listLens: &[int(sectionCount)]int{}}
	m.overlays.Push(overlay)

	updated, cmd := m.Update(SelectionChangedMsg{Target: target})
	got := updated.(Model)

	assert.Nil(t, cmd)
	assert.Same(t, target, got.store.Selected)
	require.NotNil(t, got.overlays.Top())
	assert.Equal(t, overlay.ID(), got.overlays.Top().ID())
}

func TestReduceOverlaysReplacesTopAndReturnsCommand(t *testing.T) {
	wantMsg := RefreshMsg{}
	wantCmd := func() tea.Msg { return wantMsg }
	replacement := testOverlay{id: overlays.OverlayConfirm, consumed: true}
	m := Model{}
	m.overlays.Push(testOverlay{
		id:       overlays.OverlayHelp,
		updated:  replacement,
		cmd:      wantCmd,
		consumed: true,
	})

	got, cmd, consumed := m.reduceOverlays(ShowHelpMsg{})

	assert.True(t, consumed)
	require.NotNil(t, cmd)
	assert.IsType(t, wantMsg, cmd())
	require.NotNil(t, got.overlays.Top())
	assert.Equal(t, overlays.OverlayConfirm, got.overlays.Top().ID())
}

func TestReduceOverlaysWithoutOverlayDeclinesMessage(t *testing.T) {
	m := Model{}

	got, cmd, consumed := m.reduceOverlays(ShowHelpMsg{})

	assert.False(t, consumed)
	assert.Nil(t, cmd)
	assert.True(t, got.overlays.IsEmpty())
}

func TestRefreshTickSupersedesOutstandingGeneration(t *testing.T) {
	m := Model{effectGen: 7}
	m.store.Generation = 7

	stale, cmd := m.reduceEffects(effects.RefreshTickMsg{Gen: 6})
	assert.Nil(t, cmd)
	assert.Equal(t, 7, stale.effectGen)
	assert.Equal(t, 7, stale.store.Generation)

	current, cmd := m.reduceEffects(effects.RefreshTickMsg{Gen: 7})
	assert.NotNil(t, cmd, "current tick should schedule data loads and the next tick")
	assert.Equal(t, 8, current.effectGen)
	assert.Equal(t, 8, current.store.Generation)
}

func TestBuildPaletteItemsTargetsTheSelectedNode(t *testing.T) {
	first := &domain.InspectorTarget{Kind: domain.TargetSession}
	second := &domain.InspectorTarget{Kind: domain.TargetCodeDB}
	nodes := []domain.NavNode{
		{ID: "section", Kind: domain.NavNodeSection, Label: "Sessions"},
		{ID: "first", Kind: domain.NavNodeSession, Label: "First", Target: first},
		{ID: "second", Kind: domain.NavNodeCodeIndex, Label: "Second", Target: second},
		{ID: "hint", Kind: domain.NavNodeHint, Label: "Nothing here", Target: first},
	}

	items := buildPaletteItems(nodes)
	require.Len(t, items, 4, "two selectable nodes plus refresh and help actions")

	firstMsg, ok := items[0].Cmd().(SelectionChangedMsg)
	require.True(t, ok)
	require.Same(t, first, firstMsg.Target)
	secondMsg, ok := items[1].Cmd().(SelectionChangedMsg)
	require.True(t, ok)
	require.Same(t, second, secondMsg.Target, "each command must retain its own loop target")
	_, ok = items[2].Cmd().(RefreshMsg)
	assert.True(t, ok)
	_, ok = items[3].Cmd().(ShowHelpMsg)
	assert.True(t, ok)
}

func TestOpenInspectorForCursorRejectsOutOfRangeSelection(t *testing.T) {
	m := Model{section: SectionSessions, listLens: &[int(sectionCount)]int{}}
	m.store.Sessions = []session.SessionInfo{{Filename: "one.jsonl"}}
	m.cursors[SectionSessions] = 1

	got := m.openInspectorForCursor()
	assert.False(t, got.inspectorOpen)
	assert.Nil(t, got.store.Selected)

	m.cursors[SectionSessions] = 0
	got = m.openInspectorForCursor()
	assert.True(t, got.inspectorOpen)
	require.NotNil(t, got.store.Selected)
	assert.Equal(t, domain.TargetSession, got.store.Selected.Kind)
	assert.Equal(t, "one.jsonl", got.store.Selected.Session.Filename)
}
