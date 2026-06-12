package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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

// setGlobalLevel sets zerolog's process-wide level and restores the previous
// value when the test finishes, so tests don't leak state into one another.
func setGlobalLevel(t *testing.T, l zerolog.Level) {
	t.Helper()
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(l)
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })
}

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	require.NotEmpty(t, buf.Bytes(), "expected a log line")
	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))
	return m
}

func TestSlogHandler_LevelsAndMessage(t *testing.T) {
	setGlobalLevel(t, zerolog.DebugLevel)

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
	setGlobalLevel(t, zerolog.DebugLevel)
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
	setGlobalLevel(t, zerolog.DebugLevel)
	var buf bytes.Buffer

	newTestSlog(&buf).Error("boom", "err", errors.New("kaboom"))

	m := decode(t, &buf)
	assert.Equal(t, "kaboom", m["err"])
}

func TestSlogHandler_WithAttrsAndGroup(t *testing.T) {
	setGlobalLevel(t, zerolog.DebugLevel)
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
	setGlobalLevel(t, zerolog.WarnLevel)
	h := NewSlogHandler()
	assert.False(t, h.Enabled(context.Background(), slog.LevelInfo))
	assert.True(t, h.Enabled(context.Background(), slog.LevelError))

	// Below-threshold records produce no output.
	var buf bytes.Buffer
	zl := zerolog.New(&buf).Level(zerolog.WarnLevel)
	slog.New(NewSlogHandlerFrom(&zl)).Info("suppressed")
	assert.Empty(t, buf.Bytes())

	// Enabled also honors the target logger's own minimum level, not just the
	// global level, matching what Handle will actually emit.
	setGlobalLevel(t, zerolog.DebugLevel)
	zl2 := zerolog.New(&buf).Level(zerolog.ErrorLevel)
	hf := NewSlogHandlerFrom(&zl2)
	assert.False(t, hf.Enabled(context.Background(), slog.LevelInfo))
	assert.True(t, hf.Enabled(context.Background(), slog.LevelError))
}

func TestSlogHandler_EmptyKeyDropped(t *testing.T) {
	setGlobalLevel(t, zerolog.DebugLevel)
	var buf bytes.Buffer

	// A non-group attr with an empty key must be ignored per the slog contract,
	// not emitted with a dangling field name.
	newTestSlog(&buf).LogAttrs(context.Background(), slog.LevelInfo, "hi",
		slog.String("", "orphan"),
		slog.String("kept", "yes"),
	)

	m := decode(t, &buf)
	assert.Equal(t, "yes", m["kept"])
	_, hasEmpty := m[""]
	assert.False(t, hasEmpty, "attr with empty key should be dropped")
}

// BenchmarkSlog_Direct is the baseline: zerolog written directly, no slog layer.
func BenchmarkSlog_Direct(b *testing.B) {
	zl := zerolog.New(io.Discard).Level(zerolog.InfoLevel)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		zl.Info().Str("reason", "bench").Int("count", 3).Msg("hello")
	}
}

// BenchmarkSlog_HandlerFrom measures the slog handler pinned to a fixed logger
// (NewSlogHandlerFrom) — the per-call cost of the slog->zerolog adapter without
// the As() copy.
func BenchmarkSlog_HandlerFrom(b *testing.B) {
	zl := zerolog.New(io.Discard).Level(zerolog.InfoLevel)
	l := slog.New(NewSlogHandlerFrom(&zl))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.Info("hello", "reason", "bench", "count", 3)
	}
}

// BenchmarkSlog_HandlerGlobal measures the slog handler resolving As() per call
// (NewSlogHandler) — adds the documented ~112 B / 1 alloc As() copy per record.
func BenchmarkSlog_HandlerGlobal(b *testing.B) {
	SetLogger(zerolog.New(io.Discard).Level(zerolog.InfoLevel))
	l := slog.New(NewSlogHandler())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.Info("hello", "reason", "bench", "count", 3)
	}
}
