package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestSlog wires a slog.Logger onto a fixed zerolog logger writing JSON into
// buf, so tests can assert the emitted fields.
func newTestSlog(buf *bytes.Buffer) *slog.Logger {
	zl := zerolog.New(buf)
	return slog.New(NewSlogHandlerFrom(&zl))
}

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	require.NotEmpty(t, buf.Bytes(), "expected a log line")
	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))
	return m
}

func TestSlogHandler_LevelsAndMessage(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.DebugLevel)

	cases := []struct {
		log  func(l *slog.Logger)
		want string
	}{
		{func(l *slog.Logger) { l.Debug("d") }, "debug"},
		{func(l *slog.Logger) { l.Info("i") }, "info"},
		{func(l *slog.Logger) { l.Warn("w") }, "warn"},
		{func(l *slog.Logger) { l.Error("e") }, "error"},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		c.log(newTestSlog(&buf))
		m := decode(t, &buf)
		assert.Equal(t, c.want, m["level"])
	}
}

func TestSlogHandler_Attrs(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	var buf bytes.Buffer

	newTestSlog(&buf).Info("hello",
		"reason", "TestReason",
		"count", 3,
		"ok", true,
		"dur", 5*time.Second,
	)

	m := decode(t, &buf)
	assert.Equal(t, "hello", m["message"])
	assert.Equal(t, "TestReason", m["reason"])
	assert.EqualValues(t, 3, m["count"])
	assert.Equal(t, true, m["ok"])
}

func TestSlogHandler_Error(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	var buf bytes.Buffer

	newTestSlog(&buf).Error("boom", "err", errors.New("kaboom"))

	m := decode(t, &buf)
	assert.Equal(t, "kaboom", m["err"])
}

func TestSlogHandler_WithAttrsAndGroup(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	var buf bytes.Buffer

	l := newTestSlog(&buf).
		With("service", "daemon").
		WithGroup("req").
		With("id", "abc")
	l.Info("served", "method", "GET")

	m := decode(t, &buf)
	assert.Equal(t, "daemon", m["service"], "pre-group attr keeps bare key")
	assert.Equal(t, "abc", m["req.id"], "attr added after WithGroup is prefixed")
	assert.Equal(t, "GET", m["req.method"], "record attr is prefixed by active group")
}

func TestSlogHandler_Enabled(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.WarnLevel)
	h := NewSlogHandler()
	assert.False(t, h.Enabled(context.Background(), slog.LevelInfo))
	assert.True(t, h.Enabled(context.Background(), slog.LevelError))

	// Below-threshold records produce no output.
	zerolog.SetGlobalLevel(zerolog.WarnLevel)
	var buf bytes.Buffer
	zl := zerolog.New(&buf).Level(zerolog.WarnLevel)
	slog.New(NewSlogHandlerFrom(&zl)).Info("suppressed")
	assert.Empty(t, buf.Bytes())
}
