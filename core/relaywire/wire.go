// Package relaywire defines the JSON message types exchanged between a
// ClawdChan node and the relay over a WebSocket connection. It is shared by
// the client transport and the relay server.
package relaywire

// Frame is the relay-forwarded unit on the authenticated /link endpoint.
// Client → relay: set To (destination NodeID, base64-std), leave From empty.
// Relay → client: set From (source NodeID, base64-std), leave To empty.
// Data is the opaque ciphertext payload, base64-std.
type Frame struct {
	To   string `json:"to,omitempty"`
	From string `json:"from,omitempty"`
	Data string `json:"data"`
}

// Ctl is a lightweight control message used by the relay to inform the client
// of events such as queued-frame delivery or errors.
type Ctl struct {
	Kind    string `json:"kind"`              // "error", "queued", "delivered"
	Message string `json:"message,omitempty"` // human-readable detail
}

// Envelope is a wrapper so the relay and client can multiplex Frame and Ctl
// over one websocket channel using a single JSON shape.
type Envelope struct {
	Frame *Frame `json:"frame,omitempty"`
	Ctl   *Ctl   `json:"ctl,omitempty"`
}

// LinkAuthLabel is the domain-separation string that nodes sign along with
// their NodeID and a unix-ms timestamp to authenticate to the relay's /link
// endpoint.
const LinkAuthLabel = "clawdchan-relay-link-v1"

// LinkAuthMaxSkewMs is the maximum accepted clock skew for a /link auth
// timestamp. Timestamps older or further in the future are rejected.
const LinkAuthMaxSkewMs = 60_000

// AuthMessage returns the exact byte string a node signs to authenticate to
// the relay's /link endpoint. Both the client and the server must agree on
// this construction.
func AuthMessage(nodeID []byte, tsMs int64) []byte {
	out := make([]byte, 0, len(LinkAuthLabel)+1+len(nodeID)+8)
	out = append(out, LinkAuthLabel...)
	out = append(out, '|')
	out = append(out, nodeID...)
	var tsBuf [8]byte
	for i := 0; i < 8; i++ {
		tsBuf[7-i] = byte(tsMs >> (8 * i))
	}
	out = append(out, tsBuf[:]...)
	return out
}
