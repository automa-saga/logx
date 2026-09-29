# logx

`logx` is a pluggable logging library for Go that integrates [zerolog](https://github.com/rs/zerolog) for high-performance structured logging and [lumberjack](https://github.com/natefinch/lumberjack) for log file rotation.

This library is designed for easy integration into any Go application that wants the power of zerolog with seamless log rotation support from lumberjack. It helps with the usecase where a global logger is needed across different packages or modules, while still allowing for flexible centralized configuration and output formats.

By default, it includes process ID (e.g. pid) in the logs, which can be useful for debugging applications.

## Features

- Simple, pluggable package for structured logging
- Log levels: All levels that zerolog supports (i.e. Debug, Info, Warn, Error, Fatal, Panic, Trace)
- Log file rotation via lumberjack
- Includes process ID in logs for easier debugging
- Optional UTC timestamps, truncated source caller (`IncludeCaller`, length via `CallerFieldLength`), and full package path (`IncludePackage`)
- Console (human-readable) or structured JSON output (`ConsoleLogging`)
- Persistent global fields via `SetGlobalContext`
- Bridges the standard library `log/slog` (and `go-logr`) into the same output

## Installation

```sh
go get github.com/automa-saga/logx
```

## Usage

```go
package main

import (
	"fmt"
	"github.com/automa-saga/logx"
)

func main() {
	err := logx.Initialize(logx.LoggingConfig{
		Level:          "info",
		ConsoleLogging: true,
		FileLogging:    true,
		Directory:      "/tmp/logs/myapp",
		Filename:       "myApp.log",
		MaxSize:        10, // MB
		MaxBackups:     10,
		MaxAge:         30,
		Compress:       true,
		TimeFormat:     "2006-01-02T15:04:05.000Z07:00", // millisecond precision; defaults to time.RFC3339
		UTC:               true,                         // pin timestamps to UTC (default: local time)
		IncludeCaller:     true,                         // "caller" field, e.g. pkg/sub/file.go:42
		CallerFieldLength: 0,                            // trailing segments in "caller" (0 = default 3; 1 = file name only)
		IncludePackage:    true,                         // "package" field: full import path, e.g. github.com/org/repo/pkg
	})

	if err != nil {
		panic(err)
	}

	logx.As().Info().Msg("Application started")
	logx.As().Debug().Str("userID", "123").Msg("Debugging details")
	logx.As().Warn().Msg("This is a warning")
	logx.As().Error().Err(fmt.Errorf("test error")).Msg("An error occurred")
}

# Output (with UTC, IncludeCaller, and IncludePackage)
2025-06-27T03:08:40.123Z INF acme/myapp/main.go:52 > Application started package=github.com/acme/myapp pid=35333
2025-06-27T03:08:40.124Z DBG acme/myapp/main.go:53 > Debugging details package=github.com/acme/myapp pid=35333 userID=123
2025-06-27T03:08:40.124Z WRN acme/myapp/main.go:54 > This is a warning package=github.com/acme/myapp pid=35333
2025-06-27T03:08:40.125Z ERR acme/myapp/main.go:55 > An error occurred package=github.com/acme/myapp error="test error" pid=35333

```

## Global fields

To stamp the same fields (e.g. build metadata) onto **every** log line, register
them with `SetGlobalContext`. logx re-applies them whenever it (re)builds its
loggers, and they reach both `As()` and the slog bridge:

```go
logx.SetGlobalContext(func(c zerolog.Context) zerolog.Context {
    return c.Str("build_version", version).Str("build_commit", commit)
})
```

Prefer this over `logx.SetLogger(logx.As().With()....Logger())`: that snapshots
one logger instance, is dropped by a later `Initialize`, and is not shared with
the slog bridge.

## Custom loggers

A logger you build yourself and install with `SetLogger` (for example, one that
writes to stderr instead of stdout) does not get logx's `caller` and `package`
fields. Attach `CallerHook` to add them, following the settings from the last
`Initialize`:

```go
l := zerolog.New(os.Stderr).With().Timestamp().Logger().Hook(logx.CallerHook())
logx.SetLogger(l)
```

## Bridging log/slog and go-logr

`NewSlogHandler` routes `log/slog` (and, via `logr.FromSlogHandler`, `go-logr`
consumers such as controller-runtime) through logx's sinks, level, and global
fields:

```go
slog.SetDefault(slog.New(logx.NewSlogHandler()))

// go-logr (e.g. controller-runtime):
ctrl.SetLogger(logr.FromSlogHandler(logx.NewSlogHandler()))
```

With `IncludeCaller` enabled, the handler resolves the caller from the slog
record's PC (the real call site), not from the logging wrapper.

## Performance

`logx.As()` returns a pointer to a shallow copy of the global logger (~112 bytes,
one heap allocation). For most code this is negligible. In tight loops or
high-frequency hot paths (> 1000 calls/sec), store the logger once before the
loop to avoid the per-iteration copy.

```go
logger := logx.As()              // allocate once
for _, r := range records {
    logger.Debug().Str("id", r.ID).Msg("processing")  // 0 allocs per iteration
}
```

Benchmarks (Apple M1 Max, `go test -bench BenchmarkAs -benchmem`):

| Benchmark                          | ns/op | B/op | allocs/op | Notes                              |
|------------------------------------|-------|------|-----------|------------------------------------|
| `BenchmarkAs_PerIteration`         | 34.6  | 112  | 1         | `As()` per loop, event disabled    |
| `BenchmarkAs_StoredOnce`           | 3.2   | 0    | 0         | `As()` hoisted, event disabled     |
| `BenchmarkAs_Enabled_PerIteration` | 101.6 | 112  | 1         | `As()` per loop, event emitted     |
| `BenchmarkAs_Enabled_StoredOnce`   | 69.0  | 0    | 0         | `As()` hoisted, event emitted      |

**"Enabled" vs "disabled"** refers to whether the log level lets the event
actually be written. A call like `logger.Info()` only encodes fields and writes
output when the logger's configured level permits that severity; otherwise
zerolog short-circuits and does almost nothing.

- The **disabled** rows (`As_PerIteration`, `As_StoredOnce`) call a level below
  the threshold, so the event is a no-op. These isolate the cost of `As()`
  itself — the shallow copy and its heap allocation.
- The **enabled** rows (`As_Enabled_*`) emit the event (written to `io.Discard`
  to exclude disk I/O), so they include the full real-world cost: the `As()`
  copy plus field encoding and writing.

Either way, hoisting `As()` out of the loop removes the 112 B / 1 alloc per
iteration.

Hoisting `As()` out of the loop eliminates the 112 B / 1 alloc per iteration in
both cases. Reproduce with `go test -bench BenchmarkAs -benchmem .`.

## License

MIT License. See the `LICENSE` file for details.