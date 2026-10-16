# Methods

[Guide index](../file-tools.md)

## Reading method help

Use `modules.help.list({module:"files",query:"PDF",limit:10})`, then `modules.help.read({module:"files",method:"pdf_text"})`. Lists are short and permission-filtered; cards describe parameters, examples, results, limits and recovery. Follow `next_cursor` as `after_id` for lists, or `cursor` for cards. Paid/model functions use module `$tools` in help and remain outside JS.
## Methods

- `info`: stored MIME, size and checksum (direct metadata object, without a `data` wrapper). `pdf_info.data.info` is native pdfinfo text; `video_info.data` is native FFprobe JSON.
- `pdf_info`, `pdf_text`, `pdf_search`, `pdf_render`, `pdf_images`: metadata, text, literal/RE2 search, page/crop PNG rendering and embedded images. Use `page`, `end_page`, `offset`, `max_chars`, `query`, `regex`, `width`, `height`, `x`, `y` as appropriate. Text cursors are byte offsets; offsets must be UTF-8 boundaries. Too-small text limits return an error instead of a nonadvancing cursor. Search is line-scoped and advances only past complete lines; `max_chars` is a soft scan budget. Lines at least 64 KiB are skipped with explicit `skipped_long_lines` and `line_limit_bytes` fields. Search excerpts are bounded; use text reads to inspect surrounding content.
- `image_info`, `image_resize`, `image_crop`: JPEG/PNG/GIF first-frame metadata, resize and crop. Resize defaults to `max_edge:1280`; crop uses `x,y,width,height`. Stored-raster coordinates apply: EXIF orientation is not applied. For animated GIF use video methods.
- `video_info`, `video_storyboard`, `video_frame`, `video_audio`: metadata, sampled frames, one timestamped frame and mono 16 kHz WAV extraction. Set `start,end,interval,width,max_frames` for a storyboard; use a smaller interval for a detailed section. Frame reads use `timestamp`. Storyboards have at most 24 frames. Follow `next_cursor` as the next `start`. Audio extraction has at most 300 seconds per chunk; inspect `video_info.data.streams` for `codec_type: "audio"` first.
- `text_read`, `text_search`: UTF-8/UTF-16 reading and literal/RE2 search. Reads use byte `cursor` and `limit`; search uses `query,regex,cursor,limit` with line cursors.
- `csv_read`, `json_read`, `xml_read`, `html_read`: CSV row pages, JSON Pointer, and bounded XML/HTML text. JSON summaries may be partial without a continuation cursor. HTML does not execute scripts or fetch resources.
- `archive_list`, `archive_extract`: ZIP/TAR/GZIP entries and one named `member` extracted into an artifact. Use the returned entry name; GZIP uses the logical member `content`. Traversal, unsafe entries, excessive expansion and duplicate ZIP entries are rejected.
- `office_docx`, `office_xlsx`, `office_pptx`, `office_odf`, `epub_read`: basic text or cached cell values. XLSX takes `sheet,cursor,limit`. PPTX without `slide` uses a zero-based slide cursor and returns bounded slide previews. Select `slide:N` (one-based) to read its full text with UTF-8 byte `cursor,limit` pages up to 32 KiB. `cursor_unit` identifies the mode. EPUB lists archive paths, then reads a chosen `member`; this is not spine ordering. DOCX/ODF/EPUB text cursors are UTF-8 byte offsets.

For video understanding, read a storyboard, attach selected frames, then call `audio_transcribe` on the original handle if audio exists. Repeat with a tighter time range when needed. All files stay outside JS.
