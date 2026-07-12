package api

import (
	"strings"

	"github.com/acme/wadist/internal/receipt"
)

// evoWebhook is Evolution's callback envelope (subset we consume).
// Verified against Evolution v2 source (baileys.service.ts sendDataWebhook):
// envelope = {event, instance, data, ...}. See accessor helpers below for the
// per-event data shapes.
type evoWebhook struct {
	Event    string         `json:"event"`
	Instance string         `json:"instance"`
	Data     evoWebhookData `json:"data"`
}

// evoWebhookData is the union of fields we read across events. Evolution v2 emits
// DIFFERENT shapes per event, so we tolerate both:
//   - messages.update: FLAT — data.keyId, data.fromMe, data.status (no numeric ack,
//     no nested key). Confirmed in whatsapp.baileys.service.ts 'messages.update'.
//   - messages.upsert / send responses: NESTED — data.key.{id,fromMe}.
//   - connection.update: data.wuid holds THIS account's own JID (NOT remoteJid),
//     plus data.state ("open"/"close"/"connecting"/"refused").
//
// The accessor helpers below read whichever shape is present.
type evoWebhookData struct {
	Key       *evoKey `json:"key,omitempty"`
	KeyID     string  `json:"keyId,omitempty"`  // messages.update flat message id
	FromMe    *bool   `json:"fromMe,omitempty"` // messages.update flat direction
	Status    string  `json:"status,omitempty"`
	Ack       *int    `json:"ack,omitempty"` // legacy/tolerance; v2 update uses status
	State     string  `json:"state,omitempty"`
	RemoteJID string  `json:"remoteJid,omitempty"`
	WUID      string  `json:"wuid,omitempty"` // connection.update: own account JID
}

type evoKey struct {
	ID        string `json:"id"`
	FromMe    bool   `json:"fromMe"`
	RemoteJID string `json:"remoteJid"`
}

// msgID returns the message id from either the flat (messages.update) or nested
// (messages.upsert) shape.
func (d evoWebhookData) msgID() string {
	if d.KeyID != "" {
		return d.KeyID
	}
	if d.Key != nil {
		return d.Key.ID
	}
	return ""
}

// fromMe reports whether the message is outbound, reading either shape.
func (d evoWebhookData) fromMe() bool {
	if d.FromMe != nil {
		return *d.FromMe
	}
	if d.Key != nil {
		return d.Key.FromMe
	}
	return false
}

// ownJID returns this account's own JID on a connection.update. Evolution v2
// puts it in wuid; older/other shapes used remoteJid — fall back for tolerance.
func (d evoWebhookData) ownJID() string {
	if d.WUID != "" {
		return d.WUID
	}
	return d.RemoteJID
}

// normalizeEvent folds Evolution's two event-name spellings
// ("CONNECTION_UPDATE" vs "connection.update") into one canonical dotted form.
func normalizeEvent(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "_", ".")
}

// translateAck maps Evolution's ack semantics to a receipt.Kind. Evolution v2
// messages.update carries a STATUS STRING (no numeric ack): the Baileys status
// map yields PENDING / SERVER_ACK (sent) / DELIVERY_ACK (delivered) / READ /
// PLAYED. We still accept a numeric ack when present (tolerance for other
// shapes): 2=server(sent), 3=delivered, 4=read.
func translateAck(status string, ack *int) (receipt.Kind, bool) {
	if ack != nil {
		switch {
		case *ack >= 4:
			return receipt.Read, true
		case *ack == 3:
			return receipt.Delivered, true
		}
		return "", false // ack 2 (server) = "sent", recorded at send time
	}
	switch status {
	case "READ", "PLAYED":
		return receipt.Read, true
	case "DELIVERY_ACK", "DELIVERED":
		return receipt.Delivered, true
	}
	return "", false
}
