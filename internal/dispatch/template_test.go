package dispatch

import (
	"context"
	"errors"
	"testing"
)

func TestRenderTemplate(t *testing.T) {
	got := renderTemplate("Hi {{.name}}, code {{.code}}", map[string]any{"name": "Ada", "code": 42})
	if got != "Hi Ada, code 42" { t.Fatalf("got %q", got) }
	// missing key → zero value, no error
	if g := renderTemplate("X{{.nope}}Y", map[string]any{}); g != "XY" { t.Fatalf("missing-key got %q", g) }
}

type fakeUploader struct{ calls int }
func (f *fakeUploader) Upload(_ context.Context, jid string, data []byte, sha, mime string) (*MediaHandle, error) {
	f.calls++
	return &MediaHandle{URL: "u://" + sha, DirectPath: "/d", MediaKey: []byte("k"), FileSHA256: []byte("s"), FileEncSHA256: []byte("e"), FileLength: int64(len(data))}, nil
}

func TestResolveMedia_CachesPerAccount(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	pool, ctx := pgPool(t)
	up := &fakeUploader{}
	w := NewSendWorker(pool, nil, nil, nil, up)
	raw := []byte("imgdata")
	h1, err := w.resolveMedia(ctx, "acc@s.whatsapp.net", "sha1", "image/jpeg", raw)
	if err != nil { t.Fatalf("resolve1: %v", err) }
	h2, err := w.resolveMedia(ctx, "acc@s.whatsapp.net", "sha1", "image/jpeg", raw)
	if err != nil { t.Fatalf("resolve2: %v", err) }
	if up.calls != 1 { t.Fatalf("Upload called %d times, want 1 (cache hit)", up.calls) }
	if h1.URL != h2.URL || h2.URL != "u://sha1" { t.Fatalf("handles differ: %q %q", h1.URL, h2.URL) }
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM media_uploads WHERE account_jid='acc@s.whatsapp.net' AND media_sha='sha1'`).Scan(&n)
	if n != 1 { t.Fatalf("cache rows=%d", n) }
	_ = errors.New
}
