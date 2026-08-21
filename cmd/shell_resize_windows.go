//go:build windows

package cmd

import (
	"context"
	"os"
	"time"

	"golang.org/x/term"
)

// watchWindowSize calls onChange whenever the terminal is resized, and returns a function that
// stops watching.
//
// WINDOWS HAS NO SIGWINCH, so this polls instead. A console resize is a rare, human-paced event and
// reading the buffer size is cheap, so a second's granularity is imperceptible to a person and
// costs nothing measurable. The alternative, leaving Windows without resize handling at all, means
// every full-screen program draws at 80x24 whatever the window is.
func watchWindowSize(ctx context.Context, onChange func()) func() {
	stop := make(chan struct{})
	go func() {
		fd := int(os.Stdout.Fd())
		lastCols, lastRows, _ := term.GetSize(fd)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				cols, rows, err := term.GetSize(fd)
				if err != nil || (cols == lastCols && rows == lastRows) {
					continue
				}
				lastCols, lastRows = cols, rows
				onChange()
			}
		}
	}()
	return func() { close(stop) }
}
