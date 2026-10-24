package fileproc_test

import (
	"bytes"
	"embed"
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/fileproc"
)

//go:embed testdata/animated.* testdata/shapes.json testdata/silent-video.webm testdata/fractional.apng
var animationFixtures embed.FS

func TestFractionalAnimationDurationIsNotRounded(t *testing.T) {
	t.Parallel()
	needCommands(t, "ffmpeg", "ffprobe")
	data, err := animationFixtures.ReadFile("testdata/fractional.apng")
	require.NoError(t, err)
	input := filepath.Join(t.TempDir(), "input")
	require.NoError(t, os.WriteFile(input, data, 0600))
	frame, err := fileproc.Process(
		t.Context(),
		fileproc.Request{Operation: "video.frame", Params: json.RawMessage(`{"timestamp":0.0332,"width":64}`)},
		input,
		t.TempDir(),
	)
	require.NoError(t, err)
	require.Len(t, frame.Outputs, 1)
	assertAnimationColor(t, frame.Outputs[0].Path, false)
	_, err = fileproc.Process(
		t.Context(),
		fileproc.Request{Operation: "video.frame", Params: json.RawMessage(`{"timestamp":0.0334,"width":64}`)},
		input,
		t.TempDir(),
	)
	require.Error(t, err)
}

func TestSilentVideoBeyondAnimationFrameLimit(t *testing.T) {
	t.Parallel()
	needCommands(t, "ffmpeg", "ffprobe")
	data, err := animationFixtures.ReadFile("testdata/silent-video.webm")
	require.NoError(t, err)
	input := filepath.Join(t.TempDir(), "input")
	require.NoError(t, os.WriteFile(input, data, 0600))
	_, err = fileproc.Process(t.Context(), fileproc.Request{Operation: "video.info"}, input, t.TempDir())
	require.NoError(t, err)
	frame, err := fileproc.Process(
		t.Context(),
		fileproc.Request{Operation: "video.frame", Params: json.RawMessage(`{"timestamp":19.5,"width":64}`)},
		input,
		t.TempDir(),
	)
	require.NoError(t, err)
	require.Len(t, frame.Outputs, 1)
	assertAnimationColor(t, frame.Outputs[0].Path, true)
}

func TestLottie60FPSFrameBoundary(t *testing.T) {
	t.Parallel()
	needCommands(t, "loom-animation")
	data, err := animationFixtures.ReadFile("testdata/shapes.json")
	require.NoError(t, err)
	data = bytes.ReplaceAll(data, []byte(`"fr":1`), []byte(`"fr":60`))
	input := filepath.Join(t.TempDir(), "input")
	require.NoError(t, os.WriteFile(input, data, 0600))
	frame, err := fileproc.Process(
		t.Context(),
		fileproc.Request{Operation: "video.frame", Params: json.RawMessage(`{"timestamp":0.0166,"width":64}`)},
		input,
		t.TempDir(),
	)
	require.NoError(t, err)
	require.Len(t, frame.Outputs, 1)
	assertAnimationColor(t, frame.Outputs[0].Path, true)
}

// Every fixture is a 2-second red-to-blue animation created from synthetic source frames.
func TestAnimationFormatsUseTimedStoryboards(t *testing.T) {
	t.Parallel()
	needCommands(t, "ffmpeg", "ffprobe", "loom-animation")
	for _, format := range []string{"apng", "avif", "jxl", "heics", "webm", "mng", "tgs"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			data, err := animationFixtures.ReadFile("testdata/animated." + format)
			require.NoError(t, err)
			input := filepath.Join(t.TempDir(), "input")
			require.NoError(t, os.WriteFile(input, data, 0600))
			metadata, err := fileproc.Process(
				t.Context(),
				fileproc.Request{Operation: "video.info"},
				input,
				t.TempDir(),
			)
			require.NoError(t, err)
			var info struct {
				Format struct {
					Duration string `json:"duration"`
				} `json:"format"`
			}
			require.NoError(t, json.Unmarshal(metadata.Data, &info))
			require.Contains(t, info.Format.Duration, "2.000")
			first, err := fileproc.Process(
				t.Context(),
				fileproc.Request{
					Operation: "video.storyboard",
					Params:    json.RawMessage(`{"interval":1,"max_frames":1,"width":64}`),
				},
				input,
				t.TempDir(),
			)
			require.NoError(t, err)
			require.Len(t, first.Outputs, 1)
			require.True(t, first.Partial)
			require.Equal(t, "1.000", first.NextCursor)
			assertAnimationColor(t, first.Outputs[0].Path, true)
			second, err := fileproc.Process(
				t.Context(),
				fileproc.Request{
					Operation: "video.storyboard",
					Params:    json.RawMessage(`{"start":1,"interval":1,"max_frames":1,"width":64}`),
				},
				input,
				t.TempDir(),
			)
			require.NoError(t, err)
			require.Len(t, second.Outputs, 1)
			require.False(t, second.Partial)
			assertAnimationColor(t, second.Outputs[0].Path, false)
			// A sample inside a held frame must not advance to the next image.
			for _, sample := range []struct {
				At  float64
				Red bool
			}{{0.5, true}, {0.9996, true}, {1.5, false}, {1.9996, false}} {
				params, marshalErr := json.Marshal(map[string]any{"timestamp": sample.At, "width": 64})
				require.NoError(t, marshalErr)
				frame, frameErr := fileproc.Process(
					t.Context(),
					fileproc.Request{Operation: "video.frame", Params: params},
					input,
					t.TempDir(),
				)
				require.NoError(t, frameErr)
				require.Len(t, frame.Outputs, 1)
				assertAnimationColor(t, frame.Outputs[0].Path, sample.Red)
			}
			defaults, err := fileproc.Process(
				t.Context(),
				fileproc.Request{Operation: "video.storyboard"},
				input,
				t.TempDir(),
			)
			require.NoError(t, err)
			require.Greater(t, len(defaults.Outputs), 1)
			require.False(t, defaults.Partial)
			audio, err := fileproc.Process(t.Context(), fileproc.Request{Operation: "video.audio"}, input, t.TempDir())
			require.NoError(t, err)
			require.Empty(t, audio.Outputs)
			require.JSONEq(t, `{"has_audio":false}`, string(audio.Data))
			_, err = fileproc.Process(
				t.Context(),
				fileproc.Request{Operation: "video.frame", Params: json.RawMessage(`{"timestamp":3}`)},
				input,
				t.TempDir(),
			)
			require.Error(t, err)
		})
	}
}

func assertAnimationColor(t *testing.T, path string, red bool) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	im, _, err := image.Decode(f)
	require.NoError(t, err)
	r, _, b, _ := im.At(im.Bounds().Dx()/2, im.Bounds().Dy()/2).RGBA()
	if red {
		require.Greater(t, r, uint32(45000))
		require.Less(t, b, uint32(12000))
	} else {
		require.Greater(t, b, uint32(45000))
		require.Less(t, r, uint32(12000))
	}
}
