package cliui

import (
	"io"
	"os"

	"golang.org/x/term"
)

const (
	Reset = "\x1b[0m"
	Dim   = "\x1b[2m"
	Bold  = "\x1b[1m"
	Green = "\x1b[32m"
	Red   = "\x1b[31m"
	Amber = "\x1b[33m"
	Cyan  = "\x1b[36m"
)

type Tone uint8

const (
	Neutral Tone = iota
	Success
	Progress
	Attention
	Failure
)

type Styler struct {
	Enabled bool
}

func New(w io.Writer) Styler {
	f, ok := w.(*os.File)
	return Styler{Enabled: ok && ColorEnabled(f)}
}

func ColorEnabled(f *os.File) bool {
	return colorAllowed(term.IsTerminal(int(f.Fd())), os.Getenv("NO_COLOR"), os.Getenv("TERM"))
}

func colorAllowed(tty bool, noColor, termName string) bool {
	return tty && noColor == "" && termName != "dumb"
}

func (s Styler) Apply(code, value string) string {
	if !s.Enabled || value == "" {
		return value
	}
	return code + value + Reset
}

func (s Styler) Bold(value string) string {
	return s.Apply(Bold, value)
}

func (s Styler) Dim(value string) string {
	return s.Apply(Dim, value)
}

func (s Styler) Tone(tone Tone, value string) string {
	switch tone {
	case Success:
		return s.Apply(Green, value)
	case Progress:
		return s.Apply(Cyan, value)
	case Attention:
		return s.Apply(Amber, value)
	case Failure:
		return s.Apply(Red, value)
	default:
		return value
	}
}

func (s Styler) Marker(tone Tone) string {
	marker := "○"
	switch tone {
	case Success:
		marker = "✓"
	case Progress:
		marker = "→"
	case Attention:
		marker = "!"
	case Failure:
		marker = "✗"
	}
	return s.Tone(tone, marker)
}
