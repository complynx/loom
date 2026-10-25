# Alpha fixtures

These fixtures contain synthetic shapes only. They have no external source media.

The raster sources are two 32x16 RGBA images: an opaque red/blue stripe at x=0..7,
green with alpha 128 at x=8..15, and transparent pixels at x=16..31. Each frame lasts
one second. ImageMagick 7 creates the sources; FFmpeg 8.1.2 encodes APNG, JPEG XL,
VP8/VP9 WebM and AVIF. AVIF uses separate color and monochrome alpha streams.
MNG comes from ImageMagick; coalescing the second frame produces alpha 192 in the
green stripe because the semitransparent frames blend.

The HEIF sequence uses lossless HEVC color and mask tracks at 64x32 with the
`msf1` brand. Its mask track has a `tref/auxl` reference to the color track and an
`auxi` sample-entry property of `urn:mpeg:hevc:2015:auxid:1`. These metadata boxes
are inserted after encoding; media chunk offsets do not change because `moov`
follows the media data.

`alpha.json` derives from the existing synthetic `shapes.json`: the rectangle
covers the left half of a 64x64 canvas, and layer/fill opacity are both 50 percent.
The tests gzip these bytes to exercise Telegram TGS as well as raw Lottie JSON.
