// internal/dispatch/template.go
package dispatch

import (
	"context"
	"fmt"
	"strings"
	"text/template"
)

// renderTemplate substitutes {{.var}} from vars. Missing keys render as zero (no error).
func renderTemplate(tmpl string, vars map[string]any) string {
	// Convert to map[string]string so the zero value for missing keys is "" (not nil/<no value>).
	sv := make(map[string]string, len(vars))
	for k, v := range vars {
		sv[k] = fmt.Sprint(v)
	}
	t := template.Must(template.New("m").Option("missingkey=zero").Parse(tmpl))
	var b strings.Builder
	_ = t.Execute(&b, sv)
	return b.String()
}

// resolveMedia returns a reusable upload handle for (account, media). Cache hit
// avoids re-uploading; miss uploads once and persists. The handle is account-scoped
// (an upload is bound to the uploading account's session).
func (w *SendWorker) resolveMedia(ctx context.Context, jid, mediaSha, mime string, raw []byte) (*MediaHandle, error) {
	var h MediaHandle
	err := w.pool.QueryRow(ctx, `
SELECT url, direct_path, media_key, file_sha256, file_enc_sha256, file_length
  FROM media_uploads WHERE account_jid=$1 AND media_sha=$2`, jid, mediaSha).
		Scan(&h.URL, &h.DirectPath, &h.MediaKey, &h.FileSHA256, &h.FileEncSHA256, &h.FileLength)
	if err == nil {
		return &h, nil
	}
	up, err := w.uploader.Upload(ctx, jid, raw, mediaSha, mime)
	if err != nil {
		return nil, fmt.Errorf("upload media: %w", err)
	}
	if _, err := w.pool.Exec(ctx, `
INSERT INTO media_uploads (account_jid, media_sha, url, direct_path, media_key, file_sha256, file_enc_sha256, file_length)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (account_jid, media_sha) DO NOTHING`,
		jid, mediaSha, up.URL, up.DirectPath, up.MediaKey, up.FileSHA256, up.FileEncSHA256, up.FileLength); err != nil {
		return nil, fmt.Errorf("cache media: %w", err)
	}
	return up, nil
}
