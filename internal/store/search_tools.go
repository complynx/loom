package store

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"unicode/utf8"

	"github.com/complynx/loom/internal/domain"
)

const searchOutputBytes = 64 << 10
const searchSnippetChars = 500
const binarySearchDivisor = 2

// boundSearchResult limits model-visible data. Full events remain available through paged read.
func boundSearchResult(out domain.SearchResult, in domain.SearchRequest) domain.SearchResult {
	budget := in.MaxBytes
	if budget <= 0 || budget > searchMaxOutputBytes {
		budget = searchOutputBytes
	}
	budget = max(budget, searchMinOutputBytes)
	chars := in.SnippetChars
	if chars <= 0 || chars > searchMaxSnippetChars {
		chars = searchSnippetChars
	}
	events := out.Events
	out.Events = []domain.ChatEvent{}
	used := searchEnvelopeBytes
	for index, event := range events {
		original := event
		event = searchExcerpt(event, in, chars)
		raw, _ := json.Marshal(event)
		if used+len(raw) > budget && len(out.Events) == 0 {
			event.Text = ""
			event.Data = nil
			raw, _ = json.Marshal(event)
		}
		if used+len(raw) > budget {
			out.Partial = true
			out.Reason = "output_budget"
			setSearchContinuation(&out, in, index)
			break
		}
		used += len(raw)
		out.Events = append(out.Events, event)
		if event.Text != original.Text || string(event.Data) != string(original.Data) {
			out.TruncatedSeqs = append(out.TruncatedSeqs, event.Seq)
		}
	}
	if len(out.TruncatedSeqs) > 0 && out.Reason == "" {
		out.Reason = "snippets"
	}
	return out
}

// ReadHistory provides bounded event excerpts with an advancing event cursor.
func (s *Store) ReadHistory(
	ctx context.Context,
	p domain.Principal,
	cid string,
	in domain.ReadHistoryRequest,
) (domain.SearchResult, error) {
	if in.EventSeq > 0 {
		events, err := s.History(ctx, p, cid, in.EventSeq-1, 1)
		if err != nil {
			return domain.SearchResult{}, err
		}
		if len(events) == 0 || events[0].Seq != in.EventSeq {
			return domain.SearchResult{}, domain.NotFound()
		}
		return boundedHistoryText(events[0], in)
	}
	events, err := s.History(ctx, p, cid, in.AfterSeq, minPositive(in.Limit, searchReadPageLimit)+1)
	if err != nil {
		return domain.SearchResult{}, err
	}
	out := domain.SearchResult{Events: []domain.ChatEvent{}}
	limit := minPositive(in.Limit, searchReadPageLimit)
	for _, event := range events {
		if in.BeforeSeq > 0 && event.Seq >= in.BeforeSeq {
			break
		}
		if len(out.Events) == limit {
			out.Partial = true
			out.Reason = "limit"
			break
		}
		out.Events = append(out.Events, event)
		out.NextCursor = event.Seq
	}
	return boundSearchResult(
		out,
		domain.SearchRequest{AfterSeq: in.AfterSeq, MaxBytes: in.MaxBytes, SnippetChars: in.SnippetChars},
	), nil
}

func minPositive(value, ceiling int) int {
	if value <= 0 || value > ceiling {
		return ceiling
	}
	return value
}

func searchExcerpt(event domain.ChatEvent, in domain.SearchRequest, chars int) domain.ChatEvent {
	if utf8.RuneCountInString(event.Text) > chars {
		text := []rune(event.Text)
		start := literalExcerptStart(event.Text, in, chars)
		event.Text = string(text[start:min(len(text), start+chars)])
	}
	if len(event.Data) > searchInlineDataBytes {
		event.Data = nil
	}
	return event
}

func literalExcerptStart(text string, in domain.SearchRequest, chars int) int {
	if in.Mode != searchLiteral && in.Mode != "" {
		return 0
	}
	pattern := regexp.QuoteMeta(in.Query)
	if !in.CaseSensitive {
		pattern = "(?i)" + pattern
	}
	match := regexp.MustCompile(pattern).FindStringIndex(text)
	if match == nil {
		return 0
	}
	return max(0, utf8.RuneCountInString(text[:match[0]])-chars/searchExcerptLeadDivisor)
}

func boundedHistoryText(event domain.ChatEvent, in domain.ReadHistoryRequest) (domain.SearchResult, error) {
	text := []rune(event.Text)
	if in.OffsetChars < 0 || in.OffsetChars > len(text) {
		return domain.SearchResult{}, domain.NewError(http.StatusUnprocessableEntity, "invalid_text_offset")
	}
	budget := max(minPositive(in.MaxBytes, searchMaxOutputBytes), searchMinOutputBytes)
	event.Data = nil
	// Remove large optional metadata from this text-only page. The event sequence
	// still identifies the complete source record.
	event.SourceEventID = ""
	event.Action = ""
	makePage := func(end int) domain.SearchResult {
		event.Text = string(text[in.OffsetChars:end])
		return domain.SearchResult{Events: []domain.ChatEvent{event}, NextCursor: event.Seq,
			NextTextOffset: end, TextPartial: end < len(text)}
	}
	minimum, _ := json.Marshal(makePage(min(len(text), in.OffsetChars+1)))
	if len(minimum) > budget {
		event.SourceActor = ""
		event.RunID = ""
	}
	low, high := in.OffsetChars, min(len(text), in.OffsetChars+minPositive(in.SnippetChars, searchMaxSnippetChars))
	for low < high {
		mid := low + (high-low+1)/binarySearchDivisor
		raw, _ := json.Marshal(makePage(mid))
		if len(raw) <= budget {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return makePage(low), nil
}
func setSearchContinuation(out *domain.SearchResult, in domain.SearchRequest, index int) {
	if in.Mode == searchSemantic {
		out.NextOffset = in.Offset + index
		return
	}
	out.NextCursor = in.AfterSeq
	if len(out.Events) > 0 {
		out.NextCursor = out.Events[len(out.Events)-1].Seq
	}
}
