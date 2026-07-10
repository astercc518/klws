package dispatch

import (
	"context"
	"errors"
	"fmt"
)

// ErrEvoMediaUnsupported is returned by evoSender for media sends: whatsmeow's
// MediaHandle (an encrypted WA-CDN handle) is meaningless to Evolution/Baileys,
// so media awaits the Uploader redesign in a later module.
var ErrEvoMediaUnsupported = errors.New("dispatch: evolution media send not yet supported")

// evoSendAPI is the slice of the Evolution client evoSender needs. Defined here
// (not imported from cluster) because cluster already imports dispatch — the
// production wiring supplies an adapter over *cluster.EvoClient.SendText that
// unwraps its SendResult to the returned remoteID.
type evoSendAPI interface {
	SendText(ctx context.Context, instanceName, toPhone, body string) (remoteID string, err error)
}

// InstanceRoute maps an account JID to its Evolution instance name (production:
// store.Manager.InstanceForJID). ok=false means no instance is bound yet.
type InstanceRoute func(ctx context.Context, jid string) (instanceName string, ok bool, err error)

// evoSender implements dispatch.Sender over Evolution's REST API. It routes a
// jid to its instance and returns the WA key.id as the message id (receipt
// symmetry). DORMANT: not wired into the live SendWorker in E2.
type evoSender struct {
	api   evoSendAPI
	route InstanceRoute
}

func NewEvoSender(api evoSendAPI, route InstanceRoute) *evoSender {
	return &evoSender{api: api, route: route}
}

var _ Sender = (*evoSender)(nil)

func (s *evoSender) Send(ctx context.Context, jid, phone, body string, media *MediaHandle) (string, error) {
	if media != nil {
		return "", ErrEvoMediaUnsupported
	}
	inst, ok, err := s.route(ctx, jid)
	if err != nil {
		return "", fmt.Errorf("evoSender: route jid %s: %w", jid, err)
	}
	if !ok {
		return "", fmt.Errorf("evoSender: no instance for jid %s", jid)
	}
	return s.api.SendText(ctx, inst, phone, body)
}
