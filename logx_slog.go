package logx

import (
	"context"
	"log/slog"

	"github.com/rs/zerolog"
)

// NewSlogHandler returns a slog.Handler that forwards every record to the
// package-global zerolog logger (the same one returned by As()). It lets code
// that logs through the standard library's log/slog API share logx's configured
// output — console writer, rolling file, level, pid field, and any logger set
// via SetLogger/Initialize.
//
// Typical use — make slog.Default() route through logx:
//
//	logx.Initialize(logx.LoggingConfig{
//	  Level: "info", ConsoleLogging: true,
//	  FileLogging: true, Directory: "/var/log/...", Filename: "daemon.log",
//	  MaxSize: 50, MaxBackups: 3, MaxAge: 30, Compress: true,
//	})
//
//	slog.SetDefault(slog.New(logx.NewSlogHandler()))
//
// The handler resolves As() on each Handle/Enabled call, so it always reflects
// the current logx configuration even if Initialize or SetLogger is invoked
// after the handler is created. This is the recommended choice for routing
// slog.Default() through logx: resolving As() per record costs no extra
// allocations (the copy does not escape Handle), so there is no performance
// reason to pin. Use NewSlogHandlerFrom only when you want to route records to a
// specific *zerolog.Logger instead.
func NewSlogHandler() slog.Handler {
	return &slogHandler{}
}

// NewSlogHandlerFrom returns a slog.Handler that forwards records to the given
// zerolog logger rather than the package-global one. Pass nil to fall back to
// As() (equivalent to NewSlogHandler).
//
// Use this when you want slog records to go to a specific logger — for example a
// sub-logger carrying extra context or a separate sink:
//
//	reqLogger := logx.As().With().Str("component", "http").Logger()
//	slog.SetDefault(slog.New(logx.NewSlogHandlerFrom(&reqLogger)))
//
// Note: the pinned logger is a snapshot. As() returns a shallow copy that shares
// the underlying writer by pointer, so a handler built from one keeps writing to
// that destination and will NOT pick up a later Initialize or SetLogger. When you
// want the handler to follow logx reconfiguration, use NewSlogHandler instead.
func NewSlogHandlerFrom(l *zerolog.Logger) slog.Handler {
	return &slogHandler{logger: l}
}

// slogHandler adapts slog.Handler onto a zerolog logger.
type slogHandler struct {
	// logger, when non-nil, is the fixed target; nil means resolve As() per call.
	logger *zerolog.Logger
	// prefix is the accumulated group path (e.g. "http.request.") applied to
	// attribute keys, since zerolog has no native group concept.
	prefix string
	// pre holds attributes captured via WithAttrs, each tagged with the group
	// prefix in effect when it was added, replayed onto every record.
	pre []preAttr
}

type preAttr struct {
	prefix string
	attr   slog.Attr
}

// zl resolves the target zerolog logger.
func (h *slogHandler) zl() *zerolog.Logger {
	if h.logger != nil {
		return h.logger
	}
	return As()
}

// Enabled reports whether records at the given level would be emitted, gating on
// both zerolog's global level and the target logger's own minimum level so it
// matches what Handle will actually emit (preserving slog's Enabled fast path).
func (h *slogHandler) Enabled(_ context.Context, l slog.Level) bool {
	lvl := zerologLevel(l)
	if lvl < zerolog.GlobalLevel() {
		return false
	}
	// Read the target logger's minimum level without allocating: for a pinned
	// logger read it directly, otherwise read the global logger's level under
	// the lock rather than taking an As() copy. This keeps the Enabled fast
	// path allocation-free for suppressed records.
	if h.logger != nil {
		return lvl >= h.logger.GetLevel()
	}
	return lvl >= loggerLevel()
}

// Handle maps the slog.Record onto a zerolog event: level, preformatted attrs
// (from WithAttrs), the record's own attrs, then the message.
func (h *slogHandler) Handle(_ context.Context, r slog.Record) error {
	e := h.zl().WithLevel(zerologLevel(r.Level))
	if e == nil {
		return nil
	}
	for _, p := range h.pre {
		appendAttr(e, p.prefix, p.attr)
	}
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(e, h.prefix, a)
		return true
	})
	e.Msg(r.Message)
	return nil
}

// WithAttrs returns a handler that prepends attrs (qualified by the current
// group prefix) to every subsequent record.
func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	nh := h.clone()
	for _, a := range attrs {
		nh.pre = append(nh.pre, preAttr{prefix: h.prefix, attr: a})
	}
	return nh
}

// WithGroup returns a handler that nests subsequent attribute keys under name
// using a dotted prefix (zerolog has no native groups).
func (h *slogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := h.clone()
	nh.prefix = h.prefix + name + "."
	return nh
}

func (h *slogHandler) clone() *slogHandler {
	pre := make([]preAttr, len(h.pre))
	copy(pre, h.pre)
	return &slogHandler{logger: h.logger, prefix: h.prefix, pre: pre}
}

// zerologLevel maps slog levels onto the four zerolog levels logx emits.
func zerologLevel(l slog.Level) zerolog.Level {
	switch {
	case l < slog.LevelInfo:
		return zerolog.DebugLevel
	case l < slog.LevelWarn:
		return zerolog.InfoLevel
	case l < slog.LevelError:
		return zerolog.WarnLevel
	default:
		return zerolog.ErrorLevel
	}
}

// appendAttr writes a single slog.Attr onto the zerolog event, applying prefix
// to the key. Groups recurse with an extended prefix; an error value is emitted
// via AnErr, which records the error's message (it does not add a stack trace —
// that requires an explicit Stack() call on the event).
func appendAttr(e *zerolog.Event, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()

	// Per the slog contract, ignore an empty Attr.
	if a.Equal(slog.Attr{}) {
		return
	}

	if a.Value.Kind() == slog.KindGroup {
		attrs := a.Value.Group()
		if len(attrs) == 0 {
			// Omit an empty group.
			return
		}
		p := prefix
		// A group with an empty key is inlined.
		if a.Key != "" {
			p = prefix + a.Key + "."
		}
		for _, ga := range attrs {
			appendAttr(e, p, ga)
		}
		return
	}

	// Per the slog contract, ignore a non-group attr with an empty key (an
	// empty-keyed group is inlined above, not dropped).
	if a.Key == "" {
		return
	}

	key := prefix + a.Key
	switch a.Value.Kind() {
	case slog.KindString:
		e.Str(key, a.Value.String())
	case slog.KindInt64:
		e.Int64(key, a.Value.Int64())
	case slog.KindUint64:
		e.Uint64(key, a.Value.Uint64())
	case slog.KindFloat64:
		e.Float64(key, a.Value.Float64())
	case slog.KindBool:
		e.Bool(key, a.Value.Bool())
	case slog.KindDuration:
		e.Dur(key, a.Value.Duration())
	case slog.KindTime:
		e.Time(key, a.Value.Time())
	default: // slog.KindAny and any unhandled kinds.
		v := a.Value.Any()
		if err, ok := v.(error); ok {
			e.AnErr(key, err)
		} else {
			e.Interface(key, v)
		}
	}
}
