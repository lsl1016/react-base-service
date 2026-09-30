package asynctask

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAsyncTaskCursorRoundTrip(t *testing.T) {
	cursor := encodeAsyncTaskCursor(123456)
	require.NotEmpty(t, cursor)
	id, err := decodeAsyncTaskCursor(cursor)
	require.NoError(t, err)
	require.Equal(t, uint(123456), id)
}

func TestDecodeAsyncTaskCursorRejectsInvalidValue(t *testing.T) {
	_, err := decodeAsyncTaskCursor("not-a-valid-cursor")
	require.Error(t, err)
}
