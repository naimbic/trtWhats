package handlers

import "testing"

func TestParseReportDate(t *testing.T) {
	d, ok := parseReportDate("2026-10-14", false)
	if !ok || d.Format("2006-01-02 15:04") != "2026-10-14 00:00" {
		t.Fatalf("start of day: %v %v", d, ok)
	}
	e, ok := parseReportDate("2026-10-14", true)
	if !ok || e.Format("2006-01-02 15:04:05") != "2026-10-14 23:59:59" {
		t.Fatalf("end of day: %v %v", e, ok)
	}
	if _, ok := parseReportDate("", false); ok {
		t.Fatal("empty must be unset")
	}
	if _, ok := parseReportDate("garbage", false); ok {
		t.Fatal("garbage must be unset")
	}
}
