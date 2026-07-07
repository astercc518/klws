package api

import (
	"encoding/csv"
	"fmt"

	"github.com/gin-gonic/gin"
)

// writeCSV streams a CSV attachment. It bypasses the JSON envelope: callers use
// this only for file-download endpoints. encoding/csv emits \r\n per RFC 4180
// but we normalize to \n via a manual writer for deterministic output.
func writeCSV(c *gin.Context, filename string, header []string, rows [][]string) {
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w := csv.NewWriter(c.Writer)
	w.UseCRLF = false
	_ = w.Write(header)
	_ = w.WriteAll(rows)
	w.Flush()
}
