package domain

// MemoryState separates always-injected micro memory from the on-demand index.
type MemoryState struct {
	Micro    MemoryDocument `json:"micro"`
	Index    MemoryDocument `json:"index"`
	Revision int64          `json:"revision"`
}
type MemoryDocument struct {
	Text    string `json:"text"`
	Version int64  `json:"version"`
}
type MemoryWrite struct {
	Text    string `json:"text"`
	Version int64  `json:"version"`
}

// MemoryEntry is a registry note or a detailed source referenced by a note.
type MemoryEntry struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	ParentKey string `json:"parent_key,omitempty"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	Body      string `json:"body,omitempty"`
	Version   int64  `json:"version"`
}
type MemoryEntryWrite struct {
	Kind      string `json:"kind"`
	ParentKey string `json:"parent_key,omitempty"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	Body      string `json:"body"`
	Version   int64  `json:"version"`
}
type MemoryListRequest struct {
	AfterKey string `json:"after_key,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Query    string `json:"query,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}
type MemoryListResult struct {
	Entries      []MemoryEntry `json:"entries"`
	NextAfterKey string        `json:"next_after_key,omitempty"`
	Partial      bool          `json:"partial"`
	Scanned      int           `json:"scanned"`
}
type MemoryReadRequest struct {
	Offset int `json:"offset,omitempty"`
	Limit  int `json:"limit,omitempty"`
}
type MemoryReadResult struct {
	Entry      MemoryEntry `json:"entry"`
	Offset     int         `json:"offset"`
	NextOffset int         `json:"next_offset"`
	TotalChars int         `json:"total_chars"`
	Partial    bool        `json:"partial"`
}
