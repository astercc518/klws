package api

import "testing"

func TestMapStatus(t *testing.T) {
	cases := map[string]string{
		"pending": "queued",
		"sent":    "sent",
		"failed":  "failed",
		"skipped": "skipped",
	}
	for in, want := range cases {
		if got := mapStatus(in); got != want {
			t.Errorf("mapStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClassifyError(t *testing.T) {
	// Empty → no reason/detail.
	if r, d := classifyError(""); r != nil || d != nil {
		t.Errorf("classifyError(\"\") = (%v,%v), want (nil,nil)", r, d)
	}

	cases := map[string]string{
		"send failed: wa_warning (403)":   "device_banned",
		"account banned by platform":      "device_banned",
		"number not on whatsapp":          "number_invalid",
		"invalid recipient":               "number_invalid",
		"billing: insufficient balance":   "billing_error",
		"admit: pacing rejected":          "rate_limited",
		"media: upload failed":            "media_error",
		"some other transient error":      "unknown",
	}
	for in, want := range cases {
		r, d := classifyError(in)
		if r == nil || *r != want {
			t.Errorf("classifyError(%q) reason = %v, want %q", in, r, want)
			continue
		}
		if d == nil || *d != in {
			t.Errorf("classifyError(%q) detail = %v, want raw passthrough", in, d)
		}
	}
}
