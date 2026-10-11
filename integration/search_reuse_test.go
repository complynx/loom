package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestPreviousRunKeepsExactPrefixAcrossWorkers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	model, engine := fakeModel(t, f, func(int) any { return finalResponse("old answer") })
	first := f.createRun(t, runRequest("", "", "Remember Amsterdam"), "first")
	require.NoError(t, engine.Execute(context.Background(), first.ID))
	input := runRequest("", first.ChatID, "Find plans near there")
	input.PreviousRunID = first.ID
	second := f.createRun(t, input, "second")
	require.Equal(t, first.ID, second.PreviousRunID)
	next, nextEngine := fakeModel(t, f, func(int) any { return finalResponse("new answer") })
	require.NoError(t, nextEngine.Execute(context.Background(), second.ID))
	var oldRequest, newRequest struct {
		Input []json.RawMessage `json:"input"`
	}
	require.NoError(t, json.Unmarshal(model.requests[0], &oldRequest))
	require.NoError(t, json.Unmarshal(next.requests[0], &newRequest))
	require.Greater(t, len(newRequest.Input), len(oldRequest.Input))
	for i := range oldRequest.Input {
		require.JSONEq(t, string(oldRequest.Input[i]), string(newRequest.Input[i]))
	}
	require.Contains(t, string(next.requests[0]), "old answer")
	require.Contains(t, string(next.requests[0]), "Find plans near there")
	require.Equal(t, domain.Completed, f.run(t, second.ID).State)
	code, raw := f.request(t, "bob", "POST", "/v1/runs", "foreign", input)
	require.Equal(t, 404, code, string(raw))
	otherCode, otherRaw := f.request(t, "alice", "POST", "/v1/chats", "", struct{}{})
	require.Equal(t, 201, otherCode)
	input.ChatID = decode[domain.Chat](t, otherRaw).ID
	code, raw = f.request(t, "alice", "POST", "/v1/runs", "different-chat", input)
	require.Equal(t, 404, code, string(raw))
}

func TestCacheMissAfterRetentionUsesFreshHistory(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, engine := fakeModel(t, f, func(int) any { return finalResponse("answer") })
	first := f.createRun(t, runRequest("", "", "expired secret context"), "first")
	require.NoError(t, engine.Execute(context.Background(), first.ID))
	_, err := f.pool.Exec(
		context.Background(),
		"DELETE FROM chat_events WHERE chat_id=$1 AND kind='message.user'",
		first.ChatID,
	)
	require.NoError(t, err)
	input := runRequest("", first.ChatID, "fresh request")
	input.PreviousRunID = first.ID
	second := f.createRun(t, input, "second")
	next, nextEngine := fakeModel(t, f, func(int) any { return finalResponse("fresh") })
	require.NoError(t, nextEngine.Execute(context.Background(), second.ID))
	require.NotContains(t, string(next.requests[0]), "expired secret context")
}

func TestLiteralSearchAndReadReturnUsefulBoundedPages(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, raw := f.request(t, "alice", "GET", "/v1/chats/default", "", nil)
	require.Equal(t, 200, code)
	chat := decode[domain.Chat](t, raw)
	text := strings.Repeat("prefix ", 300) + "Order [A-12]" + strings.Repeat(" suffix", 300)
	code, raw = f.request(
		t,
		"alice",
		"POST",
		"/v1/chats/"+chat.ID+"/events",
		"",
		domain.AppendEvent{Type: "user.action", SourceEventID: "big", Text: text},
	)
	require.Equal(t, 201, code, string(raw))
	code, raw = f.request(
		t,
		"alice",
		"POST",
		"/v1/chats/"+chat.ID+"/history/search",
		"",
		domain.SearchRequest{Query: "[A-12]", SnippetChars: 100, MaxBytes: 2048},
	)
	require.Equal(t, 200, code, string(raw))
	result := decode[domain.SearchResult](t, raw)
	require.Len(t, result.Events, 1)
	require.Contains(t, result.Events[0].Text, "[A-12]")
	require.NotEmpty(t, result.TruncatedSeqs)
	require.Less(t, len(raw), 2048)
	var rebuilt strings.Builder
	offset := 0
	for {
		code, raw = f.request(
			t,
			"alice",
			"POST",
			"/v1/chats/"+chat.ID+"/history/read",
			"",
			domain.ReadHistoryRequest{EventSeq: result.Events[0].Seq, OffsetChars: offset, SnippetChars: 100},
		)
		require.Equal(t, 200, code, string(raw))
		part := decode[domain.SearchResult](t, raw)
		rebuilt.WriteString(part.Events[0].Text)
		if !part.TextPartial {
			break
		}
		require.Greater(t, part.NextTextOffset, offset)
		offset = part.NextTextOffset
	}
	require.Equal(t, text, rebuilt.String())
	code, _ = f.request(t, "bob", "POST", "/v1/chats/"+chat.ID+"/history/read", "", domain.ReadHistoryRequest{})
	require.Equal(t, 404, code)
}

func TestSemanticVectorsAreOptionalScopedAndDeletedWithEvents(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	p := domain.Principal{
		Issuer:      "https://test.identity",
		Subject:     "alice",
		Actor:       "alice",
		OrgID:       "test-org",
		ExpiresAt:   time.Now().Add(time.Hour),
		Permissions: map[string]bool{"chat.read": true, "chat.events.append": true, "chat.embeddings.write": true},
	}
	chat, err := f.repository.DefaultChat(ctx, p)
	require.NoError(t, err)
	event, err := f.repository.AppendEvent(
		ctx,
		p,
		chat.ID,
		domain.AppendEvent{Type: "user.action", SourceEventID: "semantic", Text: "massage therapist Андрей"},
	)
	require.NoError(t, err)
	_, err = f.repository.Search(
		ctx,
		p,
		chat.ID,
		domain.SearchRequest{Mode: "semantic", EmbeddingModel: "fixture", Embedding: []float32{1, 0}},
	)
	require.Error(t, err)
	f.repository.SetSemanticSearchEnabled(true)
	err = f.repository.IndexEmbedding(
		ctx,
		p,
		chat.ID,
		domain.EventEmbedding{Seq: event.Seq, Model: "fixture", Vector: []float32{1, 0}},
	)
	var failure *domain.Error
	if errors.As(err, &failure) && failure.Code == "pgvector_unavailable" {
		t.Skip("pgvector is not installed; run dedicated vector acceptance")
	}
	require.NoError(t, err)
	result, err := f.repository.Search(
		ctx,
		p,
		chat.ID,
		domain.SearchRequest{Mode: "semantic", EmbeddingModel: "fixture", Embedding: []float32{1, 0}},
	)
	require.NoError(t, err)
	require.Len(t, result.Events, 1)
	require.Equal(t, event.Seq, result.Events[0].Seq)
	result, err = f.repository.Search(
		ctx,
		p,
		chat.ID,
		domain.SearchRequest{Mode: "semantic", EmbeddingModel: "different", Embedding: []float32{1, 0}},
	)
	require.NoError(t, err)
	require.Empty(t, result.Events)
	p.Subject = "bob"
	p.Actor = "bob"
	_, err = f.repository.Search(
		ctx,
		p,
		chat.ID,
		domain.SearchRequest{Mode: "semantic", EmbeddingModel: "fixture", Embedding: []float32{1, 0}},
	)
	require.Error(t, err)
	_, err = f.pool.Exec(ctx, "DELETE FROM chat_events WHERE chat_id=$1", chat.ID)
	require.NoError(t, err)
	var count int
	require.NoError(
		t,
		f.pool.QueryRow(ctx, "SELECT count(*) FROM event_embeddings WHERE chat_id=$1", chat.ID).Scan(&count),
	)
	require.Zero(t, count)
}

func TestHistoryTextPagesHonorSerializedByteBudget(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	p := domain.Principal{
		Issuer:      "https://test.identity",
		Subject:     "alice",
		Actor:       "alice",
		OrgID:       "test-org",
		Permissions: map[string]bool{"chat.read": true, "chat.events.append": true},
	}
	chat, err := f.repository.DefaultChat(ctx, p)
	require.NoError(t, err)
	text := strings.Repeat("\"😀", 2000)
	event, err := f.repository.AppendEvent(
		ctx,
		p,
		chat.ID,
		domain.AppendEvent{SourceEventID: "escaped-text", Type: "user.action", Text: text, OccurredAt: time.Now()},
	)
	require.NoError(t, err)
	var rebuilt strings.Builder
	offset := 0
	for {
		page, readErr := f.repository.ReadHistory(
			ctx,
			p,
			chat.ID,
			domain.ReadHistoryRequest{EventSeq: event.Seq, OffsetChars: offset, SnippetChars: 4000, MaxBytes: 1024},
		)
		require.NoError(t, readErr)
		raw, marshalErr := json.Marshal(page)
		require.NoError(t, marshalErr)
		require.LessOrEqual(t, len(raw), 1024)
		require.Greater(t, page.NextTextOffset, offset)
		rebuilt.WriteString(page.Events[0].Text)
		if !page.TextPartial {
			break
		}
		offset = page.NextTextOffset
	}
	require.Equal(t, text, rebuilt.String())
}
