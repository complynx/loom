package store

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

func vectorJSON(vector []float32) ([]byte, error) {
	if len(vector) < 1 || len(vector) > 4096 {
		return nil, domain.NewError(http.StatusUnprocessableEntity, "invalid_embedding")
	}
	nonzero := false
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, domain.NewError(http.StatusUnprocessableEntity, "invalid_embedding")
		}
		nonzero = nonzero || value != 0
	}
	if !nonzero {
		return nil, domain.NewError(http.StatusUnprocessableEntity, "invalid_embedding")
	}
	return json.Marshal(vector)
}

func (s *Store) vectorType(ctx context.Context) (string, error) {
	var schema string
	err := s.pool.QueryRow(ctx, "SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='vector'").
		Scan(&schema)
	if err != nil {
		return "", domain.NewError(http.StatusServiceUnavailable, "pgvector_unavailable")
	}
	return pgx.Identifier{schema, "vector"}.Sanitize(), nil
}

// IndexEmbedding accepts vectors produced by an authorized external indexing service.
// The harness neither calls a paid embedding provider nor invents semantic vectors.
func (s *Store) IndexEmbedding(ctx context.Context, p domain.Principal, cid string, in domain.EventEmbedding) error {
	if !s.semanticEnabled {
		return domain.NewError(http.StatusNotFound, "semantic_search_disabled")
	}
	if !p.Has("chat.embeddings.write") || !p.Has("chat.read") {
		return domain.Forbidden()
	}
	if in.Seq <= 0 || strings.TrimSpace(in.Model) == "" || len(in.Model) > 128 {
		return domain.NewError(http.StatusUnprocessableEntity, "invalid_embedding")
	}
	raw, err := vectorJSON(in.Vector)
	if err != nil {
		return err
	}
	if _, err = s.vectorType(ctx); err != nil {
		return err
	}
	events, err := s.History(ctx, p, cid, in.Seq-1, 1)
	if err != nil {
		return err
	}
	if len(events) == 0 || events[0].Seq != in.Seq {
		return domain.NotFound()
	}
	if cid == defaultChatAlias {
		chat, chatErr := s.DefaultChat(ctx, p)
		if chatErr != nil {
			return chatErr
		}
		cid = chat.ID
	}
	uid, err := s.ResolveUser(ctx, p)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(
		ctx,
		`INSERT INTO event_embeddings(chat_id,seq,model,dimensions,embedding) SELECT e.chat_id,e.seq,$3,$4,$5 FROM chat_events e JOIN chats c ON c.id=e.chat_id WHERE e.chat_id=$1 AND e.seq=$2 AND c.owner_id=$6 AND c.org_id=$7 AND c.deleted_at IS NULL ON CONFLICT(chat_id,seq,model) DO UPDATE SET dimensions=EXCLUDED.dimensions,embedding=EXCLUDED.embedding`,
		cid,
		in.Seq,
		in.Model,
		len(in.Vector),
		raw,
		uid,
		p.OrgID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.NotFound()
	}
	return nil
}

func (s *Store) searchSemantic(ctx context.Context, cid string, in domain.SearchRequest) (domain.SearchResult, error) {
	out := domain.SearchResult{Events: []domain.ChatEvent{}}
	raw, err := vectorJSON(in.Embedding)
	if err != nil {
		return out, err
	}
	if in.EmbeddingModel == "" || len(in.EmbeddingModel) > 128 || in.Offset < 0 || in.Offset > 10000 {
		return out, domain.NewError(http.StatusUnprocessableEntity, "invalid_semantic_query")
	}
	vector, err := s.vectorType(ctx)
	if err != nil {
		return out, err
	}
	// Exact cosine ranking after chat/model/dimension filters avoids ANN filter recall loss.
	query := `SELECT ` + eventColumns + ` FROM (SELECT e.seq,e.kind,e.action,e.text,e.doc,e.run_id,e.source_actor,e.source_event_id,e.occurred_at,e.received_at, (v.embedding::text::` + vector + ` OPERATOR(` + strings.TrimSuffix(
		vector,
		`"vector"`,
	) + `<=>) $6::text::` + vector + `) AS distance FROM chat_events e JOIN event_embeddings v ON v.chat_id=e.chat_id AND v.seq=e.seq JOIN chats c ON c.id=e.chat_id WHERE e.chat_id=$1 AND c.deleted_at IS NULL AND e.seq>$2 AND ($3=0 OR e.seq<$3) AND ($4='' OR e.source_actor=$4) AND (COALESCE(cardinality($5::text[]),0)=0 OR e.kind=ANY($5)) AND v.model=$7 AND v.dimensions=$8) ranked ORDER BY distance,seq LIMIT $9 OFFSET $10`
	rows, err := s.pool.Query(
		ctx,
		query,
		cid,
		in.AfterSeq,
		in.BeforeSeq,
		in.SourceServiceID,
		in.EventTypes,
		string(raw),
		in.EmbeddingModel,
		len(in.Embedding),
		in.Limit+1,
		in.Offset,
	)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var event domain.ChatEvent
		if err = rows.Scan(
			&event.Seq,
			&event.Type,
			&event.Action,
			&event.Text,
			&event.Data,
			&event.RunID,
			&event.SourceActor,
			&event.SourceEventID,
			&event.OccurredAt,
			&event.ReceivedAt,
		); err != nil {
			return out, err
		}
		if len(out.Events) == in.Limit {
			out.Partial = true
			out.Reason = "limit"
			break
		}
		out.Events = append(out.Events, event)
	}
	if out.Partial {
		out.NextOffset = in.Offset + len(out.Events)
	}
	return out, rows.Err()
}
