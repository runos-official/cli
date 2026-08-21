package workspace

import (
	"fmt"

	"github.com/coder/websocket"
)

/*
Refusal turns a close code from the session gate into something a person can act on, for the
WORKSPACE surface.

The same close-code contract that internal/vmconsole/refusal.go translates for VM sessions. It is a
SECOND translator on purpose, not a copy that drifted: a workspace and a machine fail for different
reasons behind the same code, so 4502 is "your workspace did not answer" here and "the machine did
not answer" there, and 4401 no longer mentions a rotating key because the workspace pre-shared key
is gone. The codes themselves are the shared part; the wording is the surface's own.

A refusal arrives AFTER the handshake succeeds, as a close code, because a browser can read no body
from a refused upgrade. Without this the person sees the library's own "failed to get reader:
received close frame", which names neither what was refused nor what to do, and retries the same
thing.
*/
func Refusal(err error) error {
	switch websocket.CloseStatus(err) {
	case 4401:
		// One message for every cause the gate collapses into 4401: expired, forged, wrong key,
		// or a pass minted for a different person's workspace. A pass lives sixty seconds and the
		// CLI mints a fresh one per attempt, so retrying is genuinely the fix. NO mention of
		// rotating a key or resetting anything in the console: that was the pre-gate PSK model and
		// telling a user to do it now sends them somewhere that changes nothing.
		return fmt.Errorf("the cluster refused this session. A session pass lasts a minute, so try again")
	case 4403:
		return fmt.Errorf("this session was minted for a different cluster, or from a page this cluster does not accept")
	case 4409:
		return fmt.Errorf("this session pass has already been used. Each one opens exactly one session; try again")
	case 4429:
		return fmt.Errorf("too many refused sessions from here recently. Wait a moment, then try again")
	case 4503:
		return fmt.Errorf("this cluster is already running as many sessions as it allows. Try again shortly")
	case 4502:
		// The far end, not the gate: the workspace pod is starting, restarting, or wedged.
		return fmt.Errorf("the cluster accepted the session but your workspace did not answer. If it was just created, give it a moment")
	case 4001:
		return fmt.Errorf("the cluster's session service is restarting. Try again in a moment")
	case websocket.StatusMessageTooBig:
		return fmt.Errorf("the session sent more in one message than it is allowed to")
	}
	return err
}
