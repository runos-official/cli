//go:build !windows

package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// watchWindowSize calls onChange whenever the terminal is resized, and returns a function that
// stops watching.
//
// SPLIT BY PLATFORM BECAUSE SIGWINCH DOES NOT EXIST ON WINDOWS. The first version of this used
// syscall.SIGWINCH inline and built fine on macOS and Linux; the release pipeline builds
// windows/amd64 too and failed there with "undefined: syscall.SIGWINCH". A local build passing on
// three of four platforms says nothing about the fourth.
func watchWindowSize(ctx context.Context, onChange func()) func() {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-winch:
				onChange()
			}
		}
	}()
	return func() { signal.Stop(winch) }
}
