# Animation storyboards

[Guide index](../file-tools.md)

Use `video_info`, `video_storyboard` and `video_frame` for animation files. These
methods return the same metadata, temporary frame handles and cursors as video
processing. Send selected output frames with standalone `artifact_attach`; transparent frames include a separate labeled alpha mask alongside the unchanged frame bytes.
Do not send an animated original as an `input_image`: a still-image decoder can
hide motion or display a separate cover image.

## Supported formats

| Input | Processing |
| --- | --- |
| Animated GIF | FFmpeg decodes the timed frames. |
| Animated WebP | The worker composes frame offsets, alpha, blend and disposal; FFmpeg decodes each static frame payload. |
| APNG (`.png`, `.apng`) | FFmpeg decodes animation frames; frame timestamps supply missing duration metadata. |
| Animated AVIF | The worker selects the timed image track instead of a single-frame cover. |
| HEIF image sequences (`.heics`, `.heifs`) | FFmpeg decodes supported sequence tracks, including HEVC. A collection of unrelated still images is not a timed sequence. |
| Animated JPEG XL (`.jxl`) | FFmpeg decodes frames; frame timestamps supply missing duration metadata. |
| WebM, including Telegram VP9 video stickers | The video path uses libvpx for VP8/VP9 streams marked with alpha; native decoders discard the auxiliary alpha bitstream. Small silent clips use a bounded frame timeline to preserve held frames; other files keep normal video processing. |
| MNG | The native helper uses ImageMagick to coalesce frames and preserve their delays. |
| Telegram TGS and shape-only Lottie JSON | The worker expands bounded gzip input, validates self-contained JSON, and renders selected frames with rlottie. |

Detection uses file contents, not the extension or caller-declared MIME type.
TGS is gzip-compressed Lottie JSON. Lottie image/font resources and expressions
are unsupported. A path, URL or expression fails validation before rendering.
SVG, dotLottie archives and interactive animation engines are not supported.

## Frame encoding and alpha

All sampled frames use still WebP. The default `quality:90` compresses color while preserving alpha losslessly. Set `quality` from 1 to 100, or `lossless:true` for lossless decoded color and alpha. The agent can repeat `video_frame` or `video_storyboard` with different encoding options; use the original source handle and the same timestamp or range. Lossless encoding cannot restore detail already lost in the input.

APNG and JPEG XL decode alpha directly. AVIF/HEIF can store alpha in a separate auxiliary track; the worker identifies its alpha type and color-track reference and merges the planes before scaling. MNG/WebP preserve composition and disposal; TGS/Lottie renders RGBA. No background is added by default. Set `background` to `"white"`, `"black"`, `"#RRGGBB"` or `"checkerboard"` to obtain an opaque inspection view. The background is applied after scaling. See [method options](methods.md#images-and-video).

For vector TGS/Lottie, `width` selects the render size as a maximum edge, with aspect ratio preserved. The default is 1280 and the maximum is 2048. Set a larger `width` when fine vector details need inspection; encoding options apply independently.

## Sampling and limits

`video_info` returns FFprobe-compatible `format` and `streams` fields in `data`.
Inspect `codec_type:"audio"` before requesting audio. Image animations and TGS
have no audio; `video_audio` returns `has_audio:false` with no output.

Times are seconds from the start of one playback. The worker samples the image
displayed at each requested timestamp, including a frame held between updates.
An omitted animation interval uses `max(0.1, duration/max_frames)` seconds.
Ordinary video keeps its 10-second default interval; clips shorter than that
default also sample across their duration. An explicit interval is at least
0.1 seconds. Use `video_frame` for a more precise timestamp.

The default is 12 output frames; the maximum is 24. Copy numeric `next_cursor`
to `start` and keep the other options unchanged to continue a storyboard.
Loops do not cause unlimited decoding. A timestamp outside the playback fails.
An empty decoder output is an error, never a usable frame handle.

Animation timelines are limited to 1000 source frames. Ordinary silent WebM or
Matroska clips above that limit, or with an unknown frame rate, keep the normal
video path; their optional animation timeline does not make metadata or frame
extraction fail.
Native MNG/TGS and WebP
canvases are limited to 16 million pixels. WebP/MNG frames with a zero delay use
10 ms. TGS uses its declared frame rate and exclusive `op` out point. Outputs
have a maximum edge of 2048 pixels. Input files are at most 20 MiB; expanded TGS
JSON has the same limit. The existing job timeout and aggregate output limits
also apply; a file within the frame limit can still exceed those limits.

`artifact_attach` supports only still PNG, JPEG, GIF and WebP. Animated GIF,
WebP and PNG originals are detected and rejected. Other animation containers
also fail direct attachment. `image_info`, `image_resize` and `image_crop`
reject GIF/WebP/PNG animations instead of silently flattening them.

```js
const source = await modules.artifacts.open({artifact_id: "ARTIFACT_ID"});
return await modules.files.video_storyboard({
  handle_id: source.handle_id, start: 0, interval: 0.25,
  width: 640, max_frames: 8
});
```

Attach selected `outputs[].handle_id` values with the model tool
`artifact_attach({handle_id: "FRAME_HANDLE_ID"})`, outside JavaScript.

## Worker dependencies and verification

The file-worker image supplies FFmpeg and `loom-animation`. The native helper
links ImageMagick and rlottie; no browser, script runtime or network is required.
Compiler and development packages remain in a build stage. Runtime package
metadata and matching upstream sources use the existing
[worker distribution procedure](../file-worker.md#public-image-distribution).

The media tests embed small synthetic fixtures. They verify metadata, red-to-blue
frame changes, held-frame timestamps, cursors and default sampling. Run them in
the built [media runtime](../qa/files-accounting.md); missing CLI tools can skip
tests on development hosts. These fixtures prove decoding and tool behavior;
they do not establish live-model interpretation quality.
