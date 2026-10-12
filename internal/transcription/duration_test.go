package transcription_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/transcription"
)

func TestDurationReceiptKeepsExactMilliseconds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		seconds      float64
		milliseconds int64
	}{{0, 0}, {1.12, 1120}, {8.007, 8007}, {1.0001, 1001}} {
		actual, err := transcription.DurationMilliseconds(tc.seconds)
		require.NoError(t, err)
		require.Equal(t, tc.milliseconds, actual)
	}
	_, err := transcription.DurationMilliseconds(math.Inf(1))
	require.Error(t, err)
}
