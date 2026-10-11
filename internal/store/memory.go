package store

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

const (
	memoryScanLimit    = 100
	memoryNote         = "note"
	memorySource       = "source"
	memoryTitleLimit   = 200
	memorySummaryLimit = 1000
	memoryBodyLimit    = 64000
	memoryEntryLimit   = 1000
	memoryQueryLimit   = 512
)

var memoryKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)

func memoryInvalid() error { return domain.NewError(http.StatusUnprocessableEntity, "invalid_memory") }
func (s *Store) memoryOwner(ctx context.Context, p domain.Principal) (string, error) {
	if p.OrgID == "" {
		return "", domain.NewError(http.StatusUnprocessableEntity, "invalid_identity")
	}
	return s.ResolveUser(ctx, p)
}
func memoryText(text string, limit int) bool {
	return utf8.ValidString(text) && !strings.ContainsRune(text, 0) && utf8.RuneCountInString(text) <= limit
}

// MemoryState returns personal memory for the authenticated user and organization.
func (s *Store) MemoryState(ctx context.Context, p domain.Principal) (domain.MemoryState, error) {
	out := domain.MemoryState{}
	uid, err := s.memoryOwner(ctx, p)
	if err != nil {
		return out, err
	}
	err = s.pool.QueryRow(ctx, `SELECT micro,micro_version,memory_index,index_version,revision FROM user_memory WHERE owner_id=$1 AND org_id=$2`, uid, p.OrgID).
		Scan(&out.Micro.Text, &out.Micro.Version, &out.Index.Text, &out.Index.Version, &out.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	return out, err
}
func memoryLock(ctx context.Context, tx pgx.Tx, uid, org string) (int64, error) {
	_, err := tx.Exec(ctx, `INSERT INTO user_memory(owner_id,org_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, uid, org)
	if err != nil {
		return 0, err
	}
	var revision int64
	err = tx.QueryRow(ctx, `SELECT revision FROM user_memory WHERE owner_id=$1 AND org_id=$2 FOR UPDATE`, uid, org).
		Scan(&revision)
	return revision, err
}

func (s *Store) setMemoryDocument(
	ctx context.Context,
	p domain.Principal,
	in domain.MemoryWrite,
	micro bool,
) (domain.MemoryDocument, error) {
	out := domain.MemoryDocument{}
	uid, err := s.memoryOwner(ctx, p)
	if err != nil {
		return out, err
	}
	maxChars := 8000
	if micro {
		maxChars = 500
	}
	if in.Version < 0 || !memoryText(in.Text, maxChars) {
		return out, memoryInvalid()
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if _, lockErr := memoryLock(ctx, tx, uid, p.OrgID); lockErr != nil {
			return lockErr
		}
		query := `UPDATE user_memory SET memory_index=$3,index_version=index_version+1,revision=revision+1 WHERE owner_id=$1 AND org_id=$2 AND index_version=$4 RETURNING memory_index,index_version`
		if micro {
			query = `UPDATE user_memory SET micro=$3,micro_version=micro_version+1,revision=revision+1 WHERE owner_id=$1 AND org_id=$2 AND micro_version=$4 RETURNING micro,micro_version`
		}
		updateErr := tx.QueryRow(ctx, query, uid, p.OrgID, in.Text, in.Version).Scan(&out.Text, &out.Version)
		if errors.Is(updateErr, pgx.ErrNoRows) {
			return domain.Conflict("memory_version_conflict")
		}
		return updateErr
	})
	return out, err
}

func (s *Store) SetMemoryMicro(
	ctx context.Context,
	p domain.Principal,
	in domain.MemoryWrite,
) (domain.MemoryDocument, error) {
	return s.setMemoryDocument(ctx, p, in, true)
}

func (s *Store) SetMemoryIndex(
	ctx context.Context,
	p domain.Principal,
	in domain.MemoryWrite,
) (domain.MemoryDocument, error) {
	return s.setMemoryDocument(ctx, p, in, false)
}

// PutMemoryEntry uses version zero only for creation. Versions never repeat after deletion.
func (s *Store) PutMemoryEntry(
	ctx context.Context,
	p domain.Principal,
	key string,
	in domain.MemoryEntryWrite,
) (domain.MemoryEntry, error) {
	out := domain.MemoryEntry{}
	uid, err := s.memoryOwner(ctx, p)
	if err != nil {
		return out, err
	}
	if in.Kind == "" {
		in.Kind = memoryNote
	}
	if !validMemoryEntry(key, in) {
		return out, memoryInvalid()
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		revision, lockErr := memoryLock(ctx, tx, uid, p.OrgID)
		if lockErr != nil {
			return lockErr
		}
		var version int64
		findErr := tx.QueryRow(ctx, `SELECT version FROM user_memory_entries WHERE owner_id=$1 AND org_id=$2 AND key=$3`, uid, p.OrgID, key).
			Scan(&version)
		if findErr != nil && !errors.Is(findErr, pgx.ErrNoRows) {
			return findErr
		}
		if version != in.Version {
			return domain.Conflict("memory_version_conflict")
		}
		if version == 0 {
			var count int
			if countErr := tx.QueryRow(ctx, `SELECT count(*) FROM user_memory_entries WHERE owner_id=$1 AND org_id=$2`, uid, p.OrgID).
				Scan(&count); countErr != nil {
				return countErr
			}
			if count >= memoryEntryLimit {
				return domain.NewError(http.StatusUnprocessableEntity, "memory_entry_limit")
			}
		}
		_, writeErr := tx.Exec(
			ctx,
			`INSERT INTO user_memory_entries(owner_id,org_id,key,kind,parent_key,title,summary,body,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(owner_id,org_id,key) DO UPDATE SET kind=EXCLUDED.kind,parent_key=EXCLUDED.parent_key,title=EXCLUDED.title,summary=EXCLUDED.summary,body=EXCLUDED.body,version=EXCLUDED.version`,
			uid,
			p.OrgID,
			key,
			in.Kind,
			in.ParentKey,
			in.Title,
			in.Summary,
			in.Body,
			revision+1,
		)
		if writeErr != nil {
			return writeErr
		}
		_, writeErr = tx.Exec(
			ctx,
			`UPDATE user_memory SET revision=revision+1 WHERE owner_id=$1 AND org_id=$2`,
			uid,
			p.OrgID,
		)
		out = domain.MemoryEntry{
			Key:       key,
			Kind:      in.Kind,
			ParentKey: in.ParentKey,
			Title:     in.Title,
			Summary:   in.Summary,
			Body:      in.Body,
			Version:   revision + 1,
		}
		return writeErr
	})
	return out, err
}
func (s *Store) DeleteMemoryEntry(ctx context.Context, p domain.Principal, key string, version int64) error {
	uid, err := s.memoryOwner(ctx, p)
	if err != nil {
		return err
	}
	if !memoryKey.MatchString(key) || version <= 0 {
		return memoryInvalid()
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		if _, lockErr := memoryLock(ctx, tx, uid, p.OrgID); lockErr != nil {
			return lockErr
		}
		tag, deleteErr := tx.Exec(
			ctx,
			`DELETE FROM user_memory_entries WHERE owner_id=$1 AND org_id=$2 AND key=$3 AND version=$4`,
			uid,
			p.OrgID,
			key,
			version,
		)
		if deleteErr != nil {
			return deleteErr
		}
		if tag.RowsAffected() == 0 {
			return domain.Conflict("memory_version_conflict")
		}
		_, deleteErr = tx.Exec(
			ctx,
			`UPDATE user_memory SET revision=revision+1 WHERE owner_id=$1 AND org_id=$2`,
			uid,
			p.OrgID,
		)
		return deleteErr
	})
}

// ClearUserMemory atomically clears all levels after comparing the account revision.
func (s *Store) ClearUserMemory(ctx context.Context, p domain.Principal, revision int64) error {
	uid, err := s.memoryOwner(ctx, p)
	if err != nil {
		return err
	}
	if revision < 0 {
		return memoryInvalid()
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		current, lockErr := memoryLock(ctx, tx, uid, p.OrgID)
		if lockErr != nil {
			return lockErr
		}
		if current != revision {
			return domain.Conflict("memory_version_conflict")
		}
		if _, deleteErr := tx.Exec(
			ctx,
			`DELETE FROM user_memory_entries WHERE owner_id=$1 AND org_id=$2`,
			uid,
			p.OrgID,
		); deleteErr != nil {
			return deleteErr
		}
		_, clearErr := tx.Exec(
			ctx,
			`UPDATE user_memory SET micro='',memory_index='',micro_version=micro_version+1,index_version=index_version+1,revision=revision+1 WHERE owner_id=$1 AND org_id=$2`,
			uid,
			p.OrgID,
		)
		return clearErr
	})
}

// ReadMemoryEntry returns a bounded Unicode slice, with explicit continuation offsets.
func (s *Store) ReadMemoryEntry(
	ctx context.Context,
	p domain.Principal,
	key string,
	in domain.MemoryReadRequest,
) (domain.MemoryReadResult, error) {
	out := domain.MemoryReadResult{}
	uid, err := s.memoryOwner(ctx, p)
	if err != nil {
		return out, err
	}
	if !memoryKey.MatchString(key) || in.Offset < 0 || in.Offset > 64000 || in.Limit < 0 || in.Limit > 16000 {
		return out, memoryInvalid()
	}
	if in.Limit == 0 {
		in.Limit = 4000
	}
	err = s.pool.QueryRow(ctx, `SELECT key,kind,parent_key,title,summary,version,substring(body FROM $4+1 FOR $5),char_length(body) FROM user_memory_entries WHERE owner_id=$1 AND org_id=$2 AND key=$3`, uid, p.OrgID, key, in.Offset, in.Limit).
		Scan(&out.Entry.Key, &out.Entry.Kind, &out.Entry.ParentKey, &out.Entry.Title, &out.Entry.Summary, &out.Entry.Version, &out.Entry.Body, &out.TotalChars)
	if err != nil {
		return out, mapError(err)
	}
	out.Offset = in.Offset
	out.NextOffset = min(in.Offset+utf8.RuneCountInString(out.Entry.Body), out.TotalChars)
	out.Partial = out.NextOffset < out.TotalChars
	return out, nil
}

// ListMemoryEntries scans at most 100 bounded documents per call. Continue even when no match is returned.
func (s *Store) ListMemoryEntries(
	ctx context.Context,
	p domain.Principal,
	in domain.MemoryListRequest,
) (domain.MemoryListResult, error) {
	out := domain.MemoryListResult{Entries: []domain.MemoryEntry{}}
	uid, err := s.memoryOwner(ctx, p)
	if err != nil {
		return out, err
	}
	if !validMemoryList(in) {
		return out, memoryInvalid()
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	if in.Mode == "" {
		in.Mode = "literal"
	}
	var pattern *regexp.Regexp
	switch in.Mode {
	case "literal":
	case "regex":
		pattern, err = regexp.Compile(in.Query)
		if err != nil {
			return out, domain.NewError(http.StatusUnprocessableEntity, "invalid_memory_regex")
		}
	default:
		return out, memoryInvalid()
	}
	rows, err := s.pool.Query(
		ctx,
		`SELECT key,kind,parent_key,title,summary,body,version FROM user_memory_entries WHERE owner_id=$1 AND org_id=$2 AND key>$3 AND ($4='' OR kind=$4) ORDER BY key LIMIT $5`,
		uid,
		p.OrgID,
		in.AfterKey,
		in.Kind,
		memoryScanLimit+1,
	)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	last := ""
	for rows.Next() {
		if out.Scanned == memoryScanLimit || len(out.Entries) == in.Limit {
			out.Partial = true
			out.NextAfterKey = last
			break
		}
		var entry domain.MemoryEntry
		if err = rows.Scan(
			&entry.Key,
			&entry.Kind,
			&entry.ParentKey,
			&entry.Title,
			&entry.Summary,
			&entry.Body,
			&entry.Version,
		); err != nil {
			return out, err
		}
		out.Scanned++
		last = entry.Key
		text := entry.Key + "\n" + entry.Title + "\n" + entry.Summary + "\n" + entry.Body
		matched := strings.Contains(strings.ToLower(text), strings.ToLower(in.Query))
		if pattern != nil {
			matched = pattern.MatchString(text)
		}
		if matched {
			entry.Body = ""
			out.Entries = append(out.Entries, entry)
		}
	}
	return out, rows.Err()
}

// ExecutionUserMemory resolves the persisted run owner, never a caller-provided identity.
func (s *Store) ExecutionUserMemory(ctx context.Context, runID string) (string, error) {
	var micro string
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(m.micro,'') FROM runs r LEFT JOIN user_memory m ON m.owner_id=r.owner_id AND m.org_id=r.org_id WHERE r.id=$1`, runID).
		Scan(&micro)
	return micro, mapError(err)
}

func validMemoryEntry(key string, in domain.MemoryEntryWrite) bool {
	return memoryKey.MatchString(key) && in.Version >= 0 && (in.Kind == memoryNote || in.Kind == memorySource) &&
		(in.ParentKey == "" || (memoryKey.MatchString(in.ParentKey) && in.Kind == memorySource && in.ParentKey != key)) &&
		memoryText(
			in.Title,
			memoryTitleLimit,
		) && memoryText(in.Summary, memorySummaryLimit) && memoryText(in.Body, memoryBodyLimit)
}
func validMemoryList(in domain.MemoryListRequest) bool {
	validKey := in.AfterKey == "" || memoryKey.MatchString(in.AfterKey)
	validKind := in.Kind == "" || in.Kind == memoryNote || in.Kind == memorySource
	validLimit := in.Limit >= 0 && in.Limit <= memoryScanLimit
	return validKey && validKind && validLimit && memoryText(in.Query, memoryQueryLimit)
}
