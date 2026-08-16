package dynacmd

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
)

// Review 2 item 17. `--follow` progress went to stderr in every mode. In
// text mode that is the command's whole visible output, so `runos vms
// create --follow > log` wrote an empty log and a terminal full of
// progress, and any wrapper that reads stdout saw nothing. stderr is
// correct only with --json, where stdout must stay one parseable
// document (A3 / B11).
func TestFollowProgressWriter(t *testing.T) {
	newCmd := func() (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
		cmd := &cobra.Command{Use: "create"}
		out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
		cmd.SetOut(out)
		cmd.SetErr(errOut)
		return cmd, out, errOut
	}

	t.Run("text mode writes progress to stdout", func(t *testing.T) {
		cmd, out, errOut := newCmd()
		w := followProgressWriter(cmd, false)
		w.Write([]byte("running"))
		if out.String() != "running" {
			t.Errorf("stdout = %q, want the progress line", out.String())
		}
		if errOut.Len() != 0 {
			t.Errorf("stderr = %q, want nothing in text mode", errOut.String())
		}
	})

	t.Run("json mode keeps stdout for the payload", func(t *testing.T) {
		cmd, out, errOut := newCmd()
		w := followProgressWriter(cmd, true)
		w.Write([]byte("running"))
		if errOut.String() != "running" {
			t.Errorf("stderr = %q, want the progress line", errOut.String())
		}
		if out.Len() != 0 {
			t.Errorf("stdout = %q, want it left to the JSON payload", out.String())
		}
	})
}
