package vmconsole

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/coder/websocket"
)

// maxFrameBytes bounds one inbound message. A console carries keystrokes and screen updates,
// not files, so anything larger means the far end is not what this expects.
const maxFrameBytes = 8 * 1024 * 1024

// Dial opens the session a ticket was minted for.
//
// The subprotocols come from the ticket verbatim: one of them carries the ticket itself, which
// is how a credential reaches a websocket at all. It must never appear in the URL, where the
// ingress access log and the browser history both keep it.
func Dial(ctx context.Context, ticket *Ticket) (*websocket.Conn, error) {
	conn, resp, err := websocket.Dial(ctx, ticket.WebsocketURL, &websocket.DialOptions{
		Subprotocols: ticket.Subprotocols,
	})
	if err != nil {
		// A refusal before the upgrade is an ordinary HTTP response, and its status is the
		// only thing that distinguishes "your ticket is no good" from "the cluster is down".
		if resp != nil {
			return nil, fmt.Errorf("the session was refused (HTTP %d)", resp.StatusCode)
		}
		return nil, err
	}
	conn.SetReadLimit(maxFrameBytes)
	return conn, nil
}

// Pipe moves bytes between a local stream and an open session until either end finishes.
//
// EVERY frame goes out BINARY. KubeVirt silently DISCARDS a text frame on these subresources,
// with no error at any layer, so a text frame does not fail: the keystrokes simply never arrive
// and nothing anywhere says why.
//
// Frame boundaries carry no meaning in either direction. The guest echoes a byte at a time, so
// anything that assumed a frame was a message would be wrong immediately.
func Pipe(ctx context.Context, conn *websocket.Conn, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Local to remote. Runs in its own goroutine because a blocking read on stdin cannot be
	// cancelled, so this side ends when the process does.
	sendErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				if writeErr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); writeErr != nil {
					sendErr <- writeErr
					return
				}
			}
			if err != nil {
				sendErr <- nil // Local side closed: an ordinary end, not a failure.
				return
			}
		}
	}()

	// Remote to local, on this goroutine so Pipe returns when the session does.
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			cancel()
			return classifyClose(err)
		}
		if _, err := out.Write(data); err != nil {
			cancel()
			return err
		}
	}
}

// classifyClose turns the end of a session into something worth printing.
//
// A normal close is not an error: the guest hung up, or the viewer did. A 4xxx close is the
// relay EXPLAINING itself, which is the only way a reason reaches a client at all once the
// handshake has completed, so its text is passed through rather than replaced.
func classifyClose(err error) error {
	var status websocket.CloseError
	if !errors.As(err, &status) {
		return err
	}
	switch {
	case status.Code == websocket.StatusNormalClosure || status.Code == websocket.StatusGoingAway:
		return nil
	case status.Code >= 4000 && status.Reason != "":
		return fmt.Errorf("%s", status.Reason)
	default:
		return fmt.Errorf("the session ended unexpectedly (%d)", int(status.Code))
	}
}
