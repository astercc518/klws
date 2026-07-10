package api

import (
	"testing"

	"github.com/acme/wadist/internal/receipt"
)

func TestTranslateAck(t *testing.T) {
	i := func(n int) *int { return &n }
	cases := []struct {
		name   string
		status string
		ack    *int
		want   receipt.Kind
		ok     bool
	}{
		{"ack4 read", "", i(4), receipt.Read, true},
		{"ack3 delivered", "", i(3), receipt.Delivered, true},
		{"ack2 server skip", "", i(2), "", false},
		{"status READ", "READ", nil, receipt.Read, true},
		{"status DELIVERY_ACK", "DELIVERY_ACK", nil, receipt.Delivered, true},
		{"status SERVER_ACK skip", "SERVER_ACK", nil, "", false},
		{"empty both", "", nil, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k, ok := translateAck(c.status, c.ack)
			if ok != c.ok || k != c.want {
				t.Fatalf("translateAck(%q,%v)=(%q,%v) want (%q,%v)", c.status, c.ack, k, ok, c.want, c.ok)
			}
		})
	}
}

func TestNormalizeEvent(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"CONNECTION_UPDATE", "connection.update"},
		{"connection.update", "connection.update"},
		{"MESSAGES_UPDATE", "messages.update"},
	} {
		if got := normalizeEvent(c.in); got != c.want {
			t.Fatalf("normalizeEvent(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
