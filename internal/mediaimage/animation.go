// Package mediaimage inspects image containers without decoding their rasters.
package mediaimage

import (
	"bytes"
	"encoding/binary"
	"errors"
)

const (
	containerHeader     = 12
	chunkHeader         = 8
	pngChunkOverhead    = 12
	gifHeader           = 13
	gifFrameHeader      = 9
	paletteMask         = 7
	gifTrailer          = 0x3b
	gifExtension        = 0x21
	gifFrame            = 0x2c
	webPCanvasHeader    = 10
	webPAnimationHeader = 6
	webPFrameHeader     = 16
	webPCoordinateScale = 2
	maxWebPFrames       = 1000
	maxCanvasPixels     = 16_000_000
	defaultDelayMS      = 10
	stillHeader         = 30
	paletteChannels     = 3
	byteMask            = 0xff
	bitsPerByte         = 8
	stillRIFFSize       = stillHeader - chunkHeader
)

var ErrAnimated = errors.New(
	"animated image: use files.video_info and files.video_storyboard, then artifact_attach on selected output frames",
)

// Animated rejects malformed supported containers rather than treating them as stills.
func Animated(data []byte) (bool, error) {
	switch {
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return animatedGIF(data)
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		chunks, err := webPChunks(data)
		if err != nil {
			return false, err
		}
		for _, c := range chunks {
			if c.kind == "ANIM" || c.kind == "ANMF" || c.kind == "VP8X" && len(c.data) > 0 && c.data[0]&2 != 0 {
				return true, nil
			}
		}
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return animatedPNG(data)
	}
	return false, nil
}

func animatedPNG(data []byte) (bool, error) {
	for offset := 8; offset < len(data); {
		if len(data)-offset < pngChunkOverhead {
			return false, errors.New("truncated PNG chunk")
		}
		n := uint64(binary.BigEndian.Uint32(data[offset:]))
		if n+pngChunkOverhead > uint64(len(data)-offset) { // #nosec G115 -- offset is bounded by len(data).
			return false, errors.New("truncated PNG payload")
		}
		if string(data[offset+4:offset+8]) == "acTL" {
			return true, nil
		}
		offset += int(n) + pngChunkOverhead // #nosec G115 -- n fits the remaining input above.
	}
	return false, nil
}

func animatedGIF(data []byte) (bool, error) {
	if len(data) < gifHeader {
		return false, errors.New("truncated GIF header")
	}
	offset := gifHeader
	if data[10]&128 != 0 {
		offset += paletteChannels << (uint(data[10]&paletteMask) + 1)
	}
	frames := 0
	for offset < len(data) {
		tag := data[offset]
		offset++
		switch tag {
		case gifTrailer:
			return frames > 1, nil
		case gifExtension:
			if offset >= len(data) {
				return false, errors.New("truncated GIF extension")
			}
			offset++
		case gifFrame:
			var frameErr error
			offset, frameErr = skipGIFFrameHeader(data, offset)
			if frameErr != nil {
				return false, frameErr
			}
			frames++
			if frames > 1 {
				return true, nil
			}
		default:
			return false, errors.New("invalid GIF block")
		}
		var err error
		offset, err = skipGIFBlocks(data, offset)
		if err != nil {
			return false, err
		}
	}
	return false, errors.New("missing GIF trailer")
}

func skipGIFFrameHeader(data []byte, offset int) (int, error) {
	if len(data)-offset < gifFrameHeader {
		return 0, errors.New("truncated GIF frame")
	}
	packed := data[offset+8]
	offset += gifFrameHeader
	if packed&128 != 0 {
		offset += paletteChannels << (uint(packed&paletteMask) + 1)
	}
	if offset >= len(data) {
		return 0, errors.New("truncated GIF palette")
	}
	return offset + 1, nil // Skip LZW code size.
}

func skipGIFBlocks(data []byte, offset int) (int, error) {
	for {
		if offset >= len(data) {
			return 0, errors.New("truncated GIF sub-block")
		}
		n := int(data[offset])
		offset++
		if n > len(data)-offset {
			return 0, errors.New("truncated GIF sub-block payload")
		}
		offset += n
		if n == 0 {
			return offset, nil
		}
	}
}

type chunk struct {
	kind string
	data []byte
}

func webPChunks(data []byte) ([]chunk, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" ||
		uint64(binary.LittleEndian.Uint32(data[4:]))+8 != uint64(len(data)) {
		return nil, errors.New("invalid WebP container")
	}
	var chunks []chunk
	for offset := containerHeader; offset < len(data); {
		if len(data)-offset < chunkHeader {
			return nil, errors.New("truncated WebP chunk")
		}
		n := uint64(binary.LittleEndian.Uint32(data[offset+4:]))
		end := uint64(offset) + chunkHeader + n
		if end+(n&1) > uint64(len(data)) {
			return nil, errors.New("truncated WebP payload")
		}
		chunks = append(
			chunks,
			chunk{string(data[offset : offset+4]), data[offset+8 : int(end)]},
		) // #nosec G115 -- end fits len(data) above.
		offset = int(
			end + (n & 1),
		) // #nosec G115 -- padded end fits len(data) above.
	}
	return chunks, nil
}

// WebPFrame holds a timed subrectangle. Payload contains static WebP chunks.
type WebPFrame struct {
	X, Y, Width, Height, DelayMS int
	Dispose, Replace             bool
	Payload                      []byte
}

type WebPAnimation struct {
	Width, Height int
	Background    [4]byte // RGBA
	Frames        []WebPFrame
}

func uint24(data []byte) int { return int(data[0]) | int(data[1])<<8 | int(data[2])<<16 }

// ParseWebP reads animation timing and composition instructions; no pixel allocation occurs.
func ParseWebP(data []byte) (WebPAnimation, error) {
	var a WebPAnimation
	chunks, err := webPChunks(data)
	if err != nil {
		return a, err
	}
	for _, c := range chunks {
		switch c.kind {
		case "VP8X":
			if len(c.data) != webPCanvasHeader {
				return a, errors.New("invalid WebP canvas")
			}
			a.Width, a.Height = uint24(c.data[4:])+1, uint24(c.data[7:])+1
		case "ANIM":
			if len(c.data) != webPAnimationHeader {
				return a, errors.New("invalid WebP animation header")
			}
			a.Background = [4]byte{c.data[2], c.data[1], c.data[0], c.data[3]}
		case "ANMF":
			f, frameErr := parseWebPFrame(c.data)
			if frameErr != nil {
				return a, frameErr
			}
			if f.X+f.Width > a.Width || f.Y+f.Height > a.Height || len(a.Frames) >= maxWebPFrames {
				return a, errors.New("WebP frame exceeds bounds")
			}
			a.Frames = append(a.Frames, f)
		}
	}
	if a.Width < 1 || a.Height < 1 || a.Width > maxCanvasPixels/a.Height || len(a.Frames) == 0 {
		return a, errors.New("invalid or oversized WebP animation")
	}
	return a, nil
}

func parseWebPFrame(data []byte) (WebPFrame, error) {
	if len(data) < webPFrameHeader {
		return WebPFrame{}, errors.New("invalid WebP frame header")
	}
	f := WebPFrame{
		X:       uint24(data) * webPCoordinateScale,
		Y:       uint24(data[3:]) * webPCoordinateScale,
		Width:   uint24(data[6:]) + 1,
		Height:  uint24(data[9:]) + 1,
		DelayMS: uint24(data[12:]),
		Dispose: data[15]&1 != 0,
		Replace: data[15]&2 != 0,
		Payload: data[16:],
	}
	if f.DelayMS == 0 {
		f.DelayMS = defaultDelayMS
	}
	return f, nil
}

// StillWebP wraps one animation frame for the static decoder, preserving alpha.
func StillWebP(f WebPFrame) []byte {
	header := make([]byte, stillHeader+len(f.Payload))
	copy(header, "RIFF")
	binary.LittleEndian.PutUint32(
		header[4:],
		uint32(stillRIFFSize+len(f.Payload)),
	) // #nosec G115 -- source payload is bounded to 20 MiB by callers.
	copy(header[8:], "WEBPVP8X")
	binary.LittleEndian.PutUint32(header[16:], webPCanvasHeader)
	header[20] = 16 // Alpha may be present in the frame payload.
	for i := range 3 {
		header[24+i] = byte(((f.Width - 1) >> (bitsPerByte * i)) & byteMask)
		header[27+i] = byte(((f.Height - 1) >> (bitsPerByte * i)) & byteMask)
	}
	copy(header[stillHeader:], f.Payload)
	return header
}
