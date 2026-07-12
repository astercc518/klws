// Command webhook_capture prints every inbound webhook's headers + pretty JSON
// body, so the real-machine runbook can diff Evolution's actual payload against
// our assumptions. It always replies 200. NOT for production.
//
//	go run webhook_capture.go            # listens on :9999
//	# point Evolution's webhook.url at http://host.docker.internal:9999/
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
)

func main() {
	addr := ":9999"
	if v := os.Getenv("CAPTURE_ADDR"); v != "" {
		addr = v
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Printf("\n===== %s %s =====\n", r.Method, r.URL.Path)

		keys := make([]string, 0, len(r.Header))
		for k := range r.Header {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			// Authorization is the V0 auth check — always surface it.
			fmt.Printf("  %s: %s\n", k, strings.Join(r.Header[k], ", "))
		}

		var pretty bytes.Buffer
		if json.Indent(&pretty, body, "  ", "  ") == nil {
			fmt.Printf("  body:\n  %s\n", pretty.String())
		} else {
			fmt.Printf("  body(raw): %s\n", string(body))
		}
		w.WriteHeader(http.StatusOK)
	})
	fmt.Printf("webhook-capture listening on %s\n", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
