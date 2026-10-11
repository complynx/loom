package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestPersonalMemoryLifecycleAndIsolation(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := context.Background()
	p := principal("memory-user")
	empty, err := s.MemoryState(ctx, p)
	require.NoError(t, err)
	require.Zero(t, empty.Revision)
	micro, err := s.SetMemoryMicro(ctx, p, domain.MemoryWrite{Text: strings.Repeat("界", 500)})
	require.NoError(t, err)
	require.EqualValues(t, 1, micro.Version)
	_, err = s.SetMemoryMicro(ctx, p, domain.MemoryWrite{Text: "stale", Version: 0})
	code(t, err, "memory_version_conflict")
	_, err = s.SetMemoryMicro(ctx, p, domain.MemoryWrite{Text: strings.Repeat("界", 501), Version: micro.Version})
	code(t, err, "invalid_memory")
	_, err = s.SetMemoryMicro(ctx, p, domain.MemoryWrite{Text: string([]byte{255}), Version: micro.Version})
	code(t, err, "invalid_memory")
	_, err = s.SetMemoryIndex(ctx, p, domain.MemoryWrite{Text: "Read preferences, then original source."})
	require.NoError(t, err)
	note, err := s.PutMemoryEntry(
		ctx,
		p,
		"preferences",
		domain.MemoryEntryWrite{Title: "Food", Summary: "Likes tea", Body: "Choose green tea"},
	)
	require.NoError(t, err)
	require.Equal(t, "note", note.Kind)
	source, err := s.PutMemoryEntry(
		ctx,
		p,
		"food-source",
		domain.MemoryEntryWrite{Kind: "source", ParentKey: note.Key, Title: "Original", Body: "甲乙丙丁戊"},
	)
	require.NoError(t, err)
	read, err := s.ReadMemoryEntry(ctx, p, source.Key, domain.MemoryReadRequest{Offset: 1, Limit: 2})
	require.NoError(t, err)
	require.Equal(t, "乙丙", read.Entry.Body)
	require.Equal(t, 3, read.NextOffset)
	require.True(t, read.Partial)
	tail, err := s.ReadMemoryEntry(ctx, p, source.Key, domain.MemoryReadRequest{Offset: read.NextOffset, Limit: 2})
	require.NoError(t, err)
	require.Equal(t, "丁戊", tail.Entry.Body)
	require.False(t, tail.Partial)
	other := principal("memory-other")
	_, err = s.ReadMemoryEntry(ctx, other, source.Key, domain.MemoryReadRequest{})
	code(t, err, "not_found")
	otherOrg := p
	otherOrg.OrgID = "other-org"
	state, err := s.MemoryState(ctx, otherOrg)
	require.NoError(t, err)
	require.Empty(t, state.Micro.Text)
	run, err := s.CreateRun(ctx, p, "memory-run", request())
	require.NoError(t, err)
	injected, err := s.ExecutionUserMemory(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, micro.Text, injected)
	require.NoError(t, s.DeleteMemoryEntry(ctx, p, note.Key, note.Version))
	recreated, err := s.PutMemoryEntry(ctx, p, note.Key, domain.MemoryEntryWrite{Body: "new"})
	require.NoError(t, err)
	require.Greater(t, recreated.Version, note.Version)
	_, err = s.PutMemoryEntry(ctx, p, note.Key, domain.MemoryEntryWrite{Version: note.Version})
	code(t, err, "memory_version_conflict")
	state, err = s.MemoryState(ctx, p)
	require.NoError(t, err)
	require.NoError(t, s.ClearUserMemory(ctx, p, state.Revision))
	err = s.ClearUserMemory(ctx, p, state.Revision)
	code(t, err, "memory_version_conflict")
	cleared, err := s.MemoryState(ctx, p)
	require.NoError(t, err)
	require.Empty(t, cleared.Micro.Text)
	require.Empty(t, cleared.Index.Text)
	list, err := s.ListMemoryEntries(ctx, p, domain.MemoryListRequest{})
	require.NoError(t, err)
	require.Empty(t, list.Entries)
	injected, err = s.ExecutionUserMemory(ctx, run.ID)
	require.NoError(t, err)
	require.Empty(t, injected)
}

func TestPersonalMemoryBoundedSearch(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := context.Background()
	p := principal("memory-search")
	for _, key := range []string{"alpha", "beta", "gamma"} {
		_, err := s.PutMemoryEntry(ctx, p, key, domain.MemoryEntryWrite{Title: key, Body: "private long TEA body"})
		require.NoError(t, err)
	}
	first, err := s.ListMemoryEntries(ctx, p, domain.MemoryListRequest{Query: "tea", Limit: 1})
	require.NoError(t, err)
	require.Len(t, first.Entries, 1)
	require.Empty(t, first.Entries[0].Body)
	require.True(t, first.Partial)
	second, err := s.ListMemoryEntries(
		ctx,
		p,
		domain.MemoryListRequest{AfterKey: first.NextAfterKey, Query: "tea", Limit: 2},
	)
	require.NoError(t, err)
	require.Len(t, second.Entries, 2)
	require.False(t, second.Partial)
	regex, err := s.ListMemoryEntries(ctx, p, domain.MemoryListRequest{Mode: "regex", Query: "^beta"})
	require.NoError(t, err)
	require.Len(t, regex.Entries, 1)
	require.Equal(t, "beta", regex.Entries[0].Key)
	_, err = s.ListMemoryEntries(ctx, p, domain.MemoryListRequest{Mode: "regex", Query: "("})
	code(t, err, "invalid_memory_regex")
	_, err = s.ReadMemoryEntry(ctx, p, "alpha", domain.MemoryReadRequest{Limit: 16001})
	code(t, err, "invalid_memory")
	_, err = s.PutMemoryEntry(ctx, p, "../escape", domain.MemoryEntryWrite{})
	code(t, err, "invalid_memory")
	_, err = s.PutMemoryEntry(ctx, p, "huge", domain.MemoryEntryWrite{Body: strings.Repeat("x", 64001)})
	code(t, err, "invalid_memory")
	p.OrgID = ""
	_, err = s.MemoryState(ctx, p)
	code(t, err, "invalid_identity")
}

func TestPersonalMemorySearchContinuesWithoutMatches(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := context.Background()
	p := principal("memory-scan")
	_, err := s.SetMemoryMicro(ctx, p, domain.MemoryWrite{})
	require.NoError(t, err)
	uid, err := s.ResolveUser(ctx, p)
	require.NoError(t, err)
	_, err = s.Pool().
		Exec(ctx, `INSERT INTO user_memory_entries(owner_id,org_id,key,kind,title,summary,body,version) SELECT $1,$2,'entry-'||lpad(n::text,3,'0'),'note','','','ordinary',1 FROM generate_series(1,101) n`, uid, p.OrgID)
	require.NoError(t, err)
	page, err := s.ListMemoryEntries(ctx, p, domain.MemoryListRequest{Query: "absent"})
	require.NoError(t, err)
	require.Empty(t, page.Entries)
	require.True(t, page.Partial)
	require.Equal(t, 100, page.Scanned)
	require.Equal(t, "entry-100", page.NextAfterKey)
	tail, err := s.ListMemoryEntries(ctx, p, domain.MemoryListRequest{Query: "absent", AfterKey: page.NextAfterKey})
	require.NoError(t, err)
	require.False(t, tail.Partial)
	require.Equal(t, 1, tail.Scanned)
}

func TestPersonalMemoryConcurrentUpdatesPreserveWinner(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := context.Background()
	p := principal("memory-concurrent")
	micro, err := s.SetMemoryMicro(ctx, p, domain.MemoryWrite{Text: "initial"})
	require.NoError(t, err)
	errors := make(chan error, 2)
	for _, text := range []string{"first", "second"} {
		go func() {
			_, writeErr := s.SetMemoryMicro(ctx, p, domain.MemoryWrite{Text: text, Version: micro.Version})
			errors <- writeErr
		}()
	}
	first, second := <-errors, <-errors
	if first == nil {
		code(t, second, "memory_version_conflict")
	} else {
		require.NoError(t, second)
		code(t, first, "memory_version_conflict")
	}
	state, err := s.MemoryState(ctx, p)
	require.NoError(t, err)
	require.EqualValues(t, 2, state.Micro.Version)
	require.Contains(t, []string{"first", "second"}, state.Micro.Text)
}
