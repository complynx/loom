# Methods

[Guide index](../file-tools.md)

## Reading method help

Read the current permission-filtered help before calling an unfamiliar method:

```js
const choices = await modules.help.list({module: "files", query: "PDF", limit: 10});
const card = await modules.help.read({module: "files", method: "pdf_text"});
return {choices, card};
```

For list continuation, pass `next_cursor` as `after_id`. For card continuation, pass it as `cursor`. Cards describe parameters, examples, results, limits and recovery. Standalone model functions, such as `artifact_attach` and `audio_transcribe`, appear under module `$tools` in help. Call those functions through the model tool interface, outside JS.

## Methods

All methods below use `modules.files.METHOD({handle_id, ...options})`. Obtain a handle through `modules.artifacts.open` as described in [References](references.md).

Except for `info`, responses contain `data`, `outputs`, `partial` and an optional `next_cursor`. Derived files are in `outputs`; each has `artifact_id`, `handle_id`, `mime`, `size` and `name`. `info` returns `{mime, size, sha256}` directly. Read the returned `partial` flag even when outputs are present: a job can return some files before a later output fails.

### PDF

`pdf_info` returns native pdfinfo text in `data.info`. `pdf_text` reads extracted text; `pdf_search` searches it using a literal `query` or RE2 with `regex:true`. `pdf_render` renders PNG pages. `pdf_images` extracts embedded PNG images.

Pages are one-based. `page` defaults to 1, and `end_page` defaults to `page`; select at most 24 pages per call. A scan of an entire PDF therefore needs explicit page ranges. Scanned pages need rendering and model vision because text extraction does not perform OCR.

`pdf_text` and `pdf_search` use `offset` and `max_chars` (default and maximum 8192). Despite its name, `max_chars` measures bytes. Pass `next_cursor` back as numeric `offset`, keeping the page range unchanged. Text offsets must start at a UTF-8 boundary; a limit too small for the next character returns an error.

```js
const h = await modules.artifacts.open({artifact_id: "ARTIFACT_ID"});
const first = await modules.files.pdf_text({handle_id: h.handle_id, page: 1, max_chars: 4096});
if (!first.next_cursor) return first;
const next = await modules.files.pdf_text({
  handle_id: h.handle_id, page: 1, max_chars: 4096,
  offset: Number(first.next_cursor)
});
return {first, next};
```

PDF search is line-scoped and advances past complete lines; its scan budget is soft. Lines that exceed the 64 KiB search buffer are skipped and reported in `skipped_long_lines` and `line_limit_bytes`. Search excerpts are bounded. A zero-match result is not proof of absence when lines were skipped. Use text reads to inspect surrounding content.

For `pdf_render`, `width` defaults to 1280 and is at most 2048. Without `height`, it sets the rendered page's maximum edge. With `height`, `x,y,width,height` also select a pixel crop after scaling. Height is at most 2048. `pdf_images` uses `offset` as an image index, returns at most 24 images, and returns the next image index in `next_cursor`.

### Images and video

`image_info`, `image_resize` and `image_crop` support still JPEG, PNG and GIF. Animated GIF, WebP and PNG containers are rejected by these methods and by `artifact_attach`; use video methods and attach selected storyboard frames. Detection uses file content, not the filename. Coordinates use the stored raster; EXIF orientation is not applied. Resize uses `max_edge` (default 1280) and preserves aspect ratio; `allow_upscale` defaults to false. Crop takes `x,y,width,height`. Outputs can use `format:"png"` or `format:"jpeg"`; JPEG accepts `quality` from 1 to 100. Input images are limited to 16 million pixels; outputs to 4 million pixels.

`video_info` returns FFprobe-compatible `format` and `streams` metadata in `data`. Inspect `data.streams` for `codec_type:"audio"` before requesting audio. GIF, WebP, APNG, AVIF, HEIF sequences, JPEG XL, WebM, MNG and Telegram TGS use these video methods. See [Animation storyboards](animations.md) for detection, timing, decoder limits and supported Lottie content.

- `video_storyboard` samples frames using `start,end,interval,width,max_frames`. Times are seconds. Defaults are `start:0`, `interval:10`, `width:1280` and `max_frames:12`; the maximum is 24 frames. For animations and clips shorter than 10 seconds, an omitted interval uses `max(0.1, duration/max_frames)` seconds. Use a smaller explicit interval for brief movements. Pass numeric `next_cursor` as the next `start`, keeping the other options unchanged.
- `video_frame` reads one frame using `timestamp` in seconds and optional `width`. Video frame width is at most 2048.
- `video_audio` extracts mono 16 kHz WAV using `start,end`, at most 300 seconds per chunk. Without `end`, it selects up to 300 seconds from `start`. Pass numeric `next_cursor` as the next `start`. A source without audio returns `data.has_audio:false` and no WAV.

For video understanding, read a storyboard, use standalone `artifact_attach` with selected output handles, then use standalone `audio_transcribe` on the original handle if audio exists. Choose explicit `start,end` ranges for longer recordings and follow the returned cursor. See [Transcription limits](transcription-limits.md).

### Text and structured documents

`text_read` reads UTF-8 or BOM-marked UTF-16. Its string `cursor` is a byte offset in the decoded UTF-8 text, excluding the BOM. `limit` bounds returned UTF-8 bytes, up to 32 KiB. Pass `next_cursor` back unchanged as `cursor`.

`text_search` uses `query,regex,cursor,limit`. Its cursor counts lines already consumed; `limit` caps matching lines, up to 100. It searches only the first 32 KiB of a long line. Inspect `truncated_lines` and `searched_prefix_bytes_per_line` before treating an empty search as absence.

- `csv_read` pages rows with `cursor,limit`. The cursor counts rows, including a header row. A page contains at most 100 rows and 32 KiB of cell text. A single row over 100 columns or 32 KiB fails; use `text_read` for a bounded raw view.
- `json_read` selects a value using JSON `pointer` (empty means the root). A large value returns a partial summary without a continuation cursor. Use a narrower pointer to inspect omitted content.
- `xml_read` and `html_read` return extracted text using UTF-8 byte `cursor,limit`, up to 32 KiB. HTML does not execute scripts or fetch resources. XML directives and DTDs are rejected.

### Archives and office formats

`archive_list` pages ZIP, TAR and GZIP entries with `cursor,limit`, up to 100 entries per page. `archive_extract` extracts one exact `member` into a derived artifact. Use the returned entry name; a plain GZIP file uses the logical member `content`. Traversal, unsafe entries, excessive expansion and duplicate ZIP names are rejected. See [Bounds and visibility](transcription-limits.md#bounds-and-visibility).

`office_docx`, `office_odf` and `epub_read` return basic text with UTF-8 byte `cursor,limit` pages up to 32 KiB. EPUB without `member` lists archive paths; choose a returned `member` to read text. The list does not follow EPUB spine order.

`office_xlsx` without `sheet` lists worksheet archive paths. Use a returned path such as `xl/worksheets/sheet1.xml` as `sheet`; display names from Excel are not selectors. With a sheet, `cursor` counts stored rows and `limit` caps rows, up to 100 per page. Cell values are cached values or text; formulas are not executed. Oversized rows return a bounds error.

`office_pptx` without `slide` lists bounded slide previews; its cursor counts slides from zero. Select `slide:N` (one-based) for full slide text, then page using UTF-8 byte `cursor,limit`, up to 32 KiB. The returned `cursor_unit` distinguishes these modes. These readers do not render office layouts.
