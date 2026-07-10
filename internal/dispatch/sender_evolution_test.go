package dispatch

import (
	"context"
	"errors"
	"testing"
)

type fakeEvoSendAPI struct {
	lastInstance, lastPhone, lastBody string
	ret                               string
	err                               error
}

func (f *fakeEvoSendAPI) SendText(_ context.Context, inst, phone, body string) (string, error) {
	f.lastInstance, f.lastPhone, f.lastBody = inst, phone, body
	return f.ret, f.err
}

func TestEvoSender_TextRoutesAndReturnsRemoteID(t *testing.T) {
	api := &fakeEvoSendAPI{ret: "WAMID9"}
	route := func(_ context.Context, jid string) (string, bool, error) {
		if jid != "j1" {
			t.Fatalf("jid=%s", jid)
		}
		return "wa_inst_1", true, nil
	}
	s := NewEvoSender(api, route)
	id, err := s.Send(context.Background(), "j1", "15551234", "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "WAMID9" {
		t.Fatalf("id=%q", id)
	}
	if api.lastInstance != "wa_inst_1" || api.lastPhone != "15551234" || api.lastBody != "hi" {
		t.Fatalf("api got %+v", api)
	}
}

func TestEvoSender_MediaUnsupported(t *testing.T) {
	s := NewEvoSender(&fakeEvoSendAPI{}, func(_ context.Context, _ string) (string, bool, error) {
		return "wa", true, nil
	})
	_, err := s.Send(context.Background(), "j1", "1555", "hi", &MediaHandle{URL: "x"})
	if !errors.Is(err, ErrEvoMediaUnsupported) {
		t.Fatalf("want ErrEvoMediaUnsupported, got %v", err)
	}
}

func TestEvoSender_NoInstance(t *testing.T) {
	s := NewEvoSender(&fakeEvoSendAPI{}, func(_ context.Context, _ string) (string, bool, error) {
		return "", false, nil
	})
	if _, err := s.Send(context.Background(), "jX", "1555", "hi", nil); err == nil {
		t.Fatal("expected error when no instance for jid")
	}
}

func TestEvoSender_RouteError(t *testing.T) {
	routeErr := errors.New("boom")
	s := NewEvoSender(&fakeEvoSendAPI{}, func(_ context.Context, _ string) (string, bool, error) {
		return "", false, routeErr
	})
	if _, err := s.Send(context.Background(), "jX", "1555", "hi", nil); !errors.Is(err, routeErr) {
		t.Fatalf("want routeErr, got %v", err)
	}
}
