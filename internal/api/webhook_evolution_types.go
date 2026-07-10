package api

import (
	"strings"

	"github.com/acme/wadist/internal/receipt"
)

// evoWebhook is Evolution's callback envelope (subset we consume).
// TODO(evo-verify): confirm event names + data fields against real Evolution v2.
type evoWebhook struct {
	Event    string         `json:"event"`
	Instance string         `json:"instance"`
	Data     evoWebhookData `json:"data"`
}

type evoWebhookData struct {
	Key       *evoKey `json:"key,omitempty"`
	Status    string  `json:"status,omitempty"`
	Ack       *int    `json:"ack,omitempty"`
	State     string  `json:"state,omitempty"`
	RemoteJID string  `json:"remoteJid,omitempty"`
}

type evoKey struct {
	ID        string `json:"id"`
	FromMe    bool   `json:"fromMe"`
	RemoteJID string `json:"remoteJid"`
}

// normalizeEvent folds Evolution's two event-name spellings
// ("CONNECTION_UPDATE" vs "connection.update") into one canonical dotted form.
func normalizeEvent(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "_", ".")
}

// translateAck maps Baileys/Evolution ack semantics to a receipt.Kind. Baileys
// numeric ack: 2=server(sent), 3=delivered(device), 4=read. Status strings vary
// by version, so the numeric ack wins when present.
// TODO(evo-verify): confirm ack numbers + status strings against real Evolution v2.
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
