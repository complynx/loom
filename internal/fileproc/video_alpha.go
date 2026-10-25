package fileproc

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
)

const (
	bmffHeader              = 8
	bmffExtendedHeader      = 16
	sampleDescriptionHeader = 8
	visualSampleHeader      = 78
	fullBoxHeader           = 4
	trackIDOffsetV0         = 12
	trackIDOffsetV1         = 20
)

// Only libvpx decodes the auxiliary WebM alpha bitstream; native VP8/VP9 drops it.
func (p videoProbe) decoderOptions() []string {
	selected, err := p.visualStream()
	if err != nil {
		return nil
	}
	s := p.Streams[selected]
	if s.Tags["alpha_mode"] != "1" {
		return nil
	}
	var decoder string
	switch s.CodecName {
	case "vp8":
		decoder = "libvpx"
	case "vp9":
		decoder = "libvpx-vp9"
	default:
		return nil
	}
	return []string{"-c:" + strconv.Itoa(s.Index), decoder}
}

func (p videoProbe) alphaStream(selected int) int {
	color := p.Streams[selected]
	colorID, _ := strconv.ParseUint(color.ID, 0, 32)
	for i, s := range p.Streams {
		linked := s.AlphaFor == uint32(colorID) || s.AlphaFor == 0 && color.Tags["title"] == "Color"
		if s.Alpha && linked && s.Width == color.Width && s.Height == color.Height &&
			s.Frames == color.Frames && s.Duration == color.Duration {
			return i
		}
	}
	return -1
}

// AVIF/HEIF store alpha in auxiliary tracks. Use explicit alpha types and color
// references so grayscale images, depth maps and gain maps cannot become masks.
func identifyAlphaTracks(input string, probe videoProbe) (videoProbe, error) {
	f, err := os.Open(input) // #nosec G703 -- input is the worker-owned staged file.
	if err != nil {
		return probe, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxInputBytes+1))
	if err != nil {
		return probe, err
	}
	if int64(len(data)) > MaxInputBytes {
		return probe, errors.New("image sequence exceeds input limit")
	}
	tracks, err := childBoxes(data, "moov", "trak")
	if err != nil {
		return probe, err
	}
	alphaIDs := map[uint32]uint32{}
	for _, track := range tracks {
		alpha, color, trackErr := alphaTrackReference(track)
		if trackErr != nil {
			return probe, trackErr
		}
		if alpha != 0 {
			alphaIDs[alpha] = color
		}
	}
	for i := range probe.Streams {
		s := &probe.Streams[i]
		id, _ := strconv.ParseUint(s.ID, 0, 32)
		s.AlphaFor = alphaIDs[uint32(id)]
		s.Alpha = s.AlphaFor != 0 || s.Tags["title"] == "Alpha"
	}
	return probe, nil
}

func alphaTrackReference(track []byte) (uint32, uint32, error) {
	refs, err := childBoxes(track, "tref", "auxl")
	if err != nil || len(refs) == 0 {
		return 0, 0, err
	}
	alpha, err := trackHasAlphaType(track)
	if err != nil || !alpha {
		return 0, 0, err
	}
	if len(refs) != 1 || len(refs[0]) != fullBoxHeader {
		return 0, 0, errors.New("ambiguous auxiliary alpha reference")
	}
	headers, err := childBoxes(track, "tkhd")
	if err != nil {
		return 0, 0, err
	}
	if len(headers) != 1 || len(headers[0]) == 0 {
		return 0, 0, errors.New("invalid auxiliary track header")
	}
	header := headers[0]
	offset := trackIDOffsetV0
	if header[0] == 1 {
		offset = trackIDOffsetV1
	}
	if header[0] > 1 || len(header) < offset+fullBoxHeader {
		return 0, 0, errors.New("truncated auxiliary track header")
	}
	return binary.BigEndian.Uint32(header[offset:]), binary.BigEndian.Uint32(refs[0]), nil
}

func trackHasAlphaType(track []byte) (bool, error) {
	descriptions, err := childBoxes(track, "mdia", "minf", "stbl", "stsd")
	if err != nil {
		return false, err
	}
	for _, description := range descriptions {
		if len(description) < sampleDescriptionHeader {
			return false, errors.New("truncated sample description")
		}
		alpha, entryErr := descriptionHasAlphaType(description[sampleDescriptionHeader:])
		if entryErr != nil || alpha {
			return alpha, entryErr
		}
	}
	return false, nil
}

func descriptionHasAlphaType(description []byte) (bool, error) {
	for _, codec := range []string{"av01", "hvc1", "hev1"} {
		entries, err := childBoxes(description, codec)
		if err != nil {
			return false, err
		}
		for _, entry := range entries {
			if len(entry) < visualSampleHeader {
				return false, errors.New("truncated visual sample entry")
			}
			auxiliary, auxErr := childBoxes(entry[visualSampleHeader:], "auxi")
			if auxErr != nil {
				return false, auxErr
			}
			if hasAlphaType(auxiliary) {
				return true, nil
			}
		}
	}
	return false, nil
}

func hasAlphaType(auxiliary [][]byte) bool {
	for _, info := range auxiliary {
		if len(info) <= fullBoxHeader {
			continue
		}
		kind, _, terminated := strings.Cut(string(info[fullBoxHeader:]), "\x00")
		if terminated &&
			(kind == "urn:mpeg:mpegB:cicp:systems:auxiliary:alpha" || kind == "urn:mpeg:hevc:2015:auxid:1") {
			return true
		}
	}
	return false
}

// Box views borrow the bounded input; only the requested hierarchy is traversed.
func childBoxes(data []byte, path ...string) ([][]byte, error) {
	parents := [][]byte{data}
	for _, kind := range path {
		var children [][]byte
		for _, parent := range parents {
			found, err := boxPayloads(parent, kind)
			if err != nil {
				return nil, err
			}
			children = append(children, found...)
		}
		parents = children
	}
	return parents, nil
}

func boxPayloads(data []byte, kind string) ([][]byte, error) {
	var found [][]byte
	for len(data) > 0 {
		name, payload, rest, err := readBox(data)
		if err != nil {
			return nil, err
		}
		if name == kind {
			found = append(found, payload)
		}
		data = rest
	}
	return found, nil
}

func readBox(data []byte) (string, []byte, []byte, error) {
	if len(data) < bmffHeader {
		return "", nil, nil, errors.New("truncated image sequence box")
	}
	size, header := uint64(binary.BigEndian.Uint32(data)), bmffHeader
	switch size {
	case 1:
		if len(data) < bmffExtendedHeader {
			return "", nil, nil, errors.New("truncated extended image sequence box")
		}
		size, header = binary.BigEndian.Uint64(data[bmffHeader:]), bmffExtendedHeader
	case 0:
		size = uint64(len(data))
	}
	if size < uint64(header) || size > uint64(len(data)) {
		return "", nil, nil, errors.New("invalid image sequence box size")
	}
	return string(
		data[4:bmffHeader],
	), data[header:int(size)], data[int(size):], nil // #nosec G115 -- size is bounded by len(data).
}
