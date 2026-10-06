// Package logging configures the global zerolog logger.
package logging

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/config"
)

// Setup applies log.level and log.format. Without an explicit format, output is
// human-readable on a terminal and JSON (one object per line, for log shippers)
// otherwise — e.g. in containers.
func Setup(cfg config.LogConfig) error {
	level := zerolog.InfoLevel
	if cfg.Level != "" {
		l, err := zerolog.ParseLevel(strings.ToLower(cfg.Level))
		if err != nil {
			return fmt.Errorf("log.level %q: %w", cfg.Level, err)
		}
		level = l
	}
	zerolog.SetGlobalLevel(level)
	zerolog.TimeFieldFormat = time.RFC3339Nano

	format := strings.ToLower(cfg.Format)
	if format == "" {
		format = "json"
		if isTerminal(os.Stderr) {
			format = "console"
		}
	}
	switch format {
	case "json":
		log.Logger = zerolog.New(os.Stderr).With().Timestamp().Logger()
	case "console":
		log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()
	default:
		return fmt.Errorf("log.format %q must be json or console", cfg.Format)
	}
	return nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
