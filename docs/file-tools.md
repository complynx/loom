# File tools and paid transcription

Enable `file_tools` only with the private worker described in [file-worker.md](file-worker.md). Merge [the config fragment](../examples/file-worker.config.yaml) into the API configuration. Keep provider credentials in the API container. The worker receives a stream of one file and has no network, database, credential or artifact-volume access.

File tools inspect a temporary file and return bounded text, metadata or derived artifact handles. They do not load binary content into JavaScript. Transcription is a separate paid model function and is disabled in the example configuration.

Start with [References](file-tools/references.md) to open an uploaded or service-backed file. Then use [Methods](file-tools/methods.md) to choose an operation and follow its pagination contract. See [Transcription limits](file-tools/transcription-limits.md) before enabling paid audio.

Commands use the repository root unless stated otherwise. See [Contributing](../CONTRIBUTING.md) for the code map and development workflow.

For GIF, WebP, APNG, AVIF, HEIF sequences, JPEG XL, WebM, MNG and Telegram TGS,
see [Animation storyboards](file-tools/animations.md).

## Handles and access

Read [References](file-tools/references.md).

## Temporary files and external IDs

Read [References](file-tools/references.md).

## Reading method help

Read [Methods](file-tools/methods.md).

## Methods

Read [Methods](file-tools/methods.md).

## Paid audio

Read [Transcription limits](file-tools/transcription-limits.md).

## Bounds and visibility

Read [Transcription limits](file-tools/transcription-limits.md).

## Live execution limits

Read [Transcription limits](file-tools/transcription-limits.md).
