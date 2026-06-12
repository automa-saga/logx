# logx

`logx` is a pluggable logging library for Go that integrates [zerolog](https://github.com/rs/zerolog) for high-performance structured logging and [lumberjack](https://github.com/bdurand/lumberjack) for log file rotation.

This library is designed for easy integration into any Go application that wants the power of zerolog with seamless log rotation support from lumberjack. It helps with the usecase where a global logger is needed across different packages or modules, while still allowing for flexible centralized configuration and output formats.

By default, it includes process ID (e.g. pid) in the logs, which can be useful for debugging applications.

## Features

- Simple, pluggable package for structured logging
- Log levels: All levels that zerolog supports (i.e. Debug, Info, Warn, Error, Fatal, Panic, Trace)
- Log file rotation via lumberjack
- Includes process ID in logs for easier debugging

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
	})

	if err != nil {
		panic(err)
	}

	logx.As().Info().Msg("Application started")
	logx.As().Debug().Str("userID", "123").Msg("Debugging details")
	logx.As().Warn().Msg("This is a warning")
	logx.As().Error().Err(fmt.Errorf("test error")).Msg("An error occurred")
}

# Output
2025-06-27T13:08:40+10:00 INF Application started pid=35333
2025-06-27T13:08:40+10:00 DBG Debugging details pid=35333 userID=123
2025-06-27T13:08:40+10:00 WRN This is a warning pid=35333
2025-06-27T13:08:40+10:00 ERR An error occurred error="test error" pid=35333

```

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