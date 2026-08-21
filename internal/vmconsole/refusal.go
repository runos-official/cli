package vmconsole

import (
	"fmt"

	"github.com/coder/websocket"
)

/*
Refusal turns a close code from the session gate into something a person can act on.

WHY THIS EXISTS AT ALL. A refusal arrives AFTER the handshake succeeds, as a close code, because a
browser can read no body from a refused upgrade. Without a translation the user sees the library's
own words, "failed to get reader: received close frame", which names neither what was refused nor
what to do, and the next thing they do is retry the same command.

THE CODES ARE A CONTRACT with the gate, and they are deliberately few. Everything about the pass
collapses to one code on purpose, so the gate is not an oracle telling a prober which guess was
closer; the operator's copy of the real reason is in the gate's log.
*/
func Refusal(err error) error {
	switch websocket.CloseStatus(err) {
	case 4401:
		// One message for every cause: expired, forged, wrong key, malformed. Almost always the
		// first, and a pass lives sixty seconds, so retrying really is the answer.
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
		// The far end, not the gate: the machine is off, its virt-handler is wedged, or the
		// workspace is restarting.
		return fmt.Errorf("the cluster accepted the session but the machine did not answer")
	case 4001:
		return fmt.Errorf("the cluster's session service is restarting. Try again in a moment")
	case websocket.StatusMessageTooBig:
		return fmt.Errorf("the session sent more in one message than it is allowed to")
	}
	return err
}
