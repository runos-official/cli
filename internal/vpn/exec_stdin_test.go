//go:build !windows

package vpn

import (
	"strings"
	"testing"
)

func TestRunWithInputFeedsStdinAndReturnsOutput(t *testing.T) {
	out, err := runWithInput("one\ntwo\n", "cat")
	if err != nil || string(out) != "one\ntwo\n" {
		t.Errorf("out=%q err=%v, want the input echoed", out, err)
	}
}

func TestRunWithInputReportsAMissingProgram(t *testing.T) {
	if _, err := runWithInput("x", "runos-no-such-program"); err == nil {
		t.Fatal("a program that does not exist must be an error")
	}
}

func TestRunWithInputGivesUpAfterTheTimeout(t *testing.T) {
	_, err := runWithInputWithin(50*1000*1000, "x", "sleep", "5")
	if err == nil || !strings.Contains(err.Error(), "did not answer in time") {
		t.Errorf("err = %v, want a timeout", err)
	}
}
