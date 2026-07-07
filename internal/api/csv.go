package api

import (
	"encoding/csv"
	"fmt"

	"github.com/gin-gonic/gin"
)

// csvSanitize neutralizes CSV formula injection ("CSV injection"): if s starts
// with a character that a spreadsheet app (Excel/Sheets) would interpret as
// launching a formula or import directive (=, +, -, @) or with a control
// character that can be used to smuggle one past naive filters (tab, CR), a
// leading apostrophe is prepended. Spreadsheet apps render a leading
// apostrophe as a literal-string marker and do not display it, so this is
// safe for human-read values while preventing formula execution. encoding/csv
// already handles quoting of commas/quotes/newlines; this only addresses the
// leading-character formula risk, which quoting does not.
func csvSanitize(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	default:
		return s
	}
}

// writeCSV streams a CSV attachment. It bypasses the JSON envelope: callers use
// this only for file-download endpoints. encoding/csv emits \r\n per RFC 4180
// but we normalize to \n via a manual writer for deterministic output.
func writeCSV(c *gin.Context, filename string, header []string, rows [][]string) {
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w := csv.NewWriter(c.Writer)
	w.UseCRLF = false

	sanitizedHeader := make([]string, len(header))
	for i, h := range header {
		sanitizedHeader[i] = csvSanitize(h)
	}
	_ = w.Write(sanitizedHeader)

	sanitizedRows := make([][]string, len(rows))
	for i, row := range rows {
		sr := make([]string, len(row))
		for j, cell := range row {
			sr[j] = csvSanitize(cell)
		}
		sanitizedRows[i] = sr
	}
	_ = w.WriteAll(sanitizedRows)

	w.Flush()
}
