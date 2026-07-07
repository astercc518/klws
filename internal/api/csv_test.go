package api

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWriteCSV(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	writeCSV(c, "ledger.csv", []string{"id", "kind"}, [][]string{{"1", "topup"}, {"2", "settle"}})

	if ct := w.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("content-type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); cd != `attachment; filename="ledger.csv"` {
		t.Errorf("disposition = %q", cd)
	}
	want := "id,kind\n1,topup\n2,settle\n"
	if got := w.Body.String(); got != want {
		t.Errorf("body = %q want %q", got, want)
	}
}

func TestWriteCSVSanitizesFormulaInjection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	writeCSV(c, "ledger.csv", []string{"id", "kind"}, [][]string{{"1", "=1+2"}, {"2", "topup"}, {"3", "100"}})

	want := "id,kind\n1,'=1+2\n2,topup\n3,100\n"
	if got := w.Body.String(); got != want {
		t.Errorf("body = %q want %q", got, want)
	}
}
