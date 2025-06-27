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

## License

MIT License. See the `LICENSE` file for details.