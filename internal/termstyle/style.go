package termstyle

import (
	"os"

	"github.com/mattn/go-isatty"
)

// enabled tracks whether ANSI styling is active.
// Defaults to true if stdout is a TTY.
var enabled = isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())

// SetEnabled overrides the auto-detected TTY check.
func SetEnabled(on bool) {
	enabled = on
}

// Enabled returns whether styling is currently active.
func Enabled() bool {
	return enabled
}

func wrap(code, s string) string {
	if !enabled || s == "" {
		return s
	}
	return code + s + "\033[0m"
}

// Bold renders text in bold.
func Bold(s string) string { return wrap("\033[1m", s) }

// Dim renders text in dim/faint.
func Dim(s string) string { return wrap("\033[2m", s) }

// Red renders text in red.
func Red(s string) string { return wrap("\033[31m", s) }

// Green renders text in green.
func Green(s string) string { return wrap("\033[32m", s) }

// Yellow renders text in yellow.
func Yellow(s string) string { return wrap("\033[33m", s) }

// Magenta renders text in magenta.
func Magenta(s string) string { return wrap("\033[35m", s) }

// Cyan renders text in cyan.
func Cyan(s string) string { return wrap("\033[36m", s) }

// Gray renders text in gray/white.
func Gray(s string) string { return wrap("\033[37m", s) }

// Symbols for status indicators.
func GreenDot() string  { return Green("●") }
func YellowDot() string { return Yellow("○") }
func RedDot() string    { return Red("●") }
func GrayDot() string   { return Gray("○") }
func RedX() string      { return Red("✗") }
