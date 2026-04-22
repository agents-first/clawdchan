package main

import (
	"os"

	"github.com/mattn/go-isatty"
)

// colorEnabled controls whether the style helpers emit ANSI escapes.
// Evaluated once at package init: respects NO_COLOR (off), FORCE_COLOR
// (on, even when piped) and otherwise tracks stdout-is-a-TTY. This is
// the standard contract — see https://no-color.org.
var colorEnabled = func() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	return isatty.IsTerminal(os.Stdout.Fd())
}()

// style wraps s in an ANSI escape pair. When colorEnabled is false
// (non-TTY, NO_COLOR set, piped output) it returns s unchanged so
// scripts that grep our output still see plain text.
func style(s, code string) string {
	if !colorEnabled {
		return s
	}
	return code + s + "\x1b[0m"
}

func bold(s string) string   { return style(s, "\x1b[1m") }
func dim(s string) string    { return style(s, "\x1b[2m") }
func red(s string) string    { return style(s, "\x1b[31m") }
func green(s string) string  { return style(s, "\x1b[32m") }
func yellow(s string) string { return style(s, "\x1b[33m") }
func cyan(s string) string   { return style(s, "\x1b[36m") }

// okTag is the green "[ok]" marker used throughout the setup flow.
// Centralized so the look stays consistent across agent files.
func okTag() string { return green("[ok]") }

// warnTag is the yellow "[warn]" marker for non-fatal notes.
func warnTag() string { return yellow("[warn]") }
