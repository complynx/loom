package store_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
)

func TestReactionsLifecycleReplayAndIsolation(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("alice")
	in := request()
	in.Interaction.Reactions = &domain.ReactionPolicy{
		Emoji:  []string{"👍"},
		Custom: []domain.CustomReaction{{ID: "thanks", Description: "Thank you"}},
	}
	run, err := s.CreateRun(t.Context(), p, "run", in)
	require.NoError(t, err)
	run, err = s.ClaimRun(t.Context(), run.ID, "worker", time.Minute)
	require.NoError(t, err)
	history, err := s.History(t.Context(), p, run.ChatID, 0, 100)
	require.NoError(t, err)
	target := history[0].Seq
	reaction := domain.ReactionChange{MessageSeq: target, Reaction: "👍", Active: true}
	change := domain.SetReaction{SourceEventID: "add", ReactionChange: reaction}
	added, err := s.SetReaction(t.Context(), p, run.ChatID, change)
	require.NoError(t, err)
	require.Equal(t, "message.reaction", added.Type)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(added.Data, &payload))
	require.Equal(t, "user:alice", payload["actor"])
	change.SourceEventID, change.Active = "remove", false
	_, err = s.SetReaction(t.Context(), p, run.ChatID, change)
	require.NoError(t, err)
	// An old add receipt must not restore state after removal.
	change.SourceEventID, change.Active = "add", true
	replay, err := store.New(s.Pool(), nil).SetReaction(t.Context(), p, run.ChatID, change)
	require.NoError(t, err)
	require.Equal(t, added.Seq, replay.Seq)
	history, err = s.History(t.Context(), p, run.ChatID, 0, 100)
	require.NoError(t, err)
	require.Empty(t, history[0].Reactions)
	change.SourceEventID = "add-again"
	_, err = s.SetReaction(t.Context(), p, run.ChatID, change)
	require.NoError(t, err)
	botChange := domain.ReactionChange{MessageSeq: target, Reaction: "custom:thanks", Active: true}
	bot, err := s.React(t.Context(), run.ID, "worker", run.Fence, "bot-call", botChange)
	require.NoError(t, err)
	botReplay, err := s.React(t.Context(), run.ID, "worker", run.Fence, "bot-call", botChange)
	require.NoError(t, err)
	require.Equal(t, bot.Seq, botReplay.Seq)
	require.NoError(t, json.Unmarshal(bot.Data, &payload))
	require.Equal(t, "Thank you", payload["description"])
	history, err = s.History(t.Context(), p, run.ChatID, 0, 100)
	require.NoError(t, err)
	require.Len(t, history, 5)
	require.Equal(t, bot.Seq, history[0].ReactionsSeq)
	require.Equal(t, map[string][]string{"user:alice": {"👍"}, "assistant": {"custom:thanks"}}, history[0].Reactions)
	// History excerpts, fulltext search and model history carry the same attached state.
	read, err := s.ReadHistory(t.Context(), p, run.ChatID, domain.ReadHistoryRequest{Limit: 1})
	require.NoError(t, err)
	require.Equal(t, history[0].Reactions, read.Events[0].Reactions)
	search, err := s.Search(
		t.Context(),
		p,
		run.ChatID,
		domain.SearchRequest{Mode: "fulltext", Query: run.Input.Text, Limit: 10},
	)
	require.NoError(t, err)
	require.NotEmpty(t, search.Events)
	require.Equal(t, history[0].Reactions, search.Events[0].Reactions)
	contextEvents, err := s.ExecutionContextHistoryAfter(t.Context(), run.ID, 0, 100)
	require.NoError(t, err)
	require.Equal(t, history[0].Reactions, contextEvents[0].Reactions)
	current, err := s.ExecutionRun(t.Context(), run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Running, current.State)
	require.Equal(t, run.ConversationRevision, current.ConversationRevision)
	change.Active = false
	_, err = s.SetReaction(t.Context(), p, run.ChatID, change)
	code(t, err, "event_conflict")
	_, err = s.SetReaction(t.Context(), principal("bob"), run.ChatID, change)
	code(t, err, "not_found")
	otherOrg := p
	otherOrg.OrgID = "other"
	_, err = s.SetReaction(t.Context(), otherOrg, run.ChatID, change)
	code(t, err, "not_found")
	botChange.Reaction = "👎"
	_, err = s.React(t.Context(), run.ID, "worker", run.Fence, "disallowed", botChange)
	code(t, err, "reaction_not_supported")
	botChange.Reaction = "👍"
	_, err = s.React(t.Context(), run.ID, "worker", run.Fence+1, "stale", botChange)
	code(t, err, "lease_lost")
	change.SourceEventID, change.MessageSeq = "bad-target", bot.Seq
	_, err = s.SetReaction(t.Context(), p, run.ChatID, change)
	code(t, err, "invalid_reaction_target")
	var published int
	require.NoError(
		t,
		s.Pool().
			QueryRow(t.Context(), "SELECT count(*) FROM run_events WHERE run_id=$1 AND type='message.reaction'", run.ID).
			Scan(&published),
	)
	require.Equal(t, 1, published)
	botChange.Reaction, botChange.Active = "custom:thanks", false
	removed, err := s.React(t.Context(), run.ID, "worker", run.Fence, "bot-remove", botChange)
	require.NoError(t, err)
	history, err = s.History(t.Context(), p, run.ChatID, 0, 100)
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"user:alice": {"👍"}}, history[0].Reactions)
	require.Equal(t, removed.Seq, history[0].ReactionsSeq)
	botChange.Active = true
	_, err = s.React(t.Context(), run.ID, "worker", run.Fence, "bot-add-again", botChange)
	require.NoError(t, err)
}

func TestBotReactionsRequireCapabilityAndKeepUserState(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("alice")
	run, err := s.CreateRun(t.Context(), p, "run", request())
	require.NoError(t, err)
	run, err = s.ClaimRun(t.Context(), run.ID, "worker", time.Minute)
	require.NoError(t, err)
	change := domain.ReactionChange{MessageSeq: 1, Reaction: "👍", Active: true}
	_, err = s.React(t.Context(), run.ID, "worker", run.Fence, "call", change)
	code(t, err, "reaction_not_supported")
	_, err = s.SetReaction(
		t.Context(),
		p,
		run.ChatID,
		domain.SetReaction{SourceEventID: "user", ReactionChange: change},
	)
	require.NoError(t, err)
	denied := p
	denied.Permissions = map[string]bool{"chat.read": true}
	_, err = s.SetReaction(
		t.Context(),
		denied,
		run.ChatID,
		domain.SetReaction{SourceEventID: "denied", ReactionChange: change},
	)
	code(t, err, "forbidden")
	require.NoError(t, s.Transition(t.Context(), run.ID, "worker", run.Fence, domain.Completed, "", "done"))
	// User reactions remain passive and available after the run ends.
	change.MessageSeq, change.Active = 3, true
	_, err = s.SetReaction(
		t.Context(),
		p,
		run.ChatID,
		domain.SetReaction{SourceEventID: "on-reply", ReactionChange: change},
	)
	require.NoError(t, err)
}
