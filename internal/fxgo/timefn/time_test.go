package timefn

import (
	"testing"
	"time"
)

func TestParseTimeInLocation(t *testing.T) {
	loc := time.FixedZone("Test", 8*60*60)
	got := ParseTimeInLocation(DateFormatWithHypen, "2026-06-10", loc)
	if got.IsError() {
		t.Fatalf("ParseTimeInLocation returned error: %v", got.Error())
	}
	if got.MustGet().Location() != loc {
		t.Fatalf("location = %v, want %v", got.MustGet().Location(), loc)
	}
	if got.MustGet().Format(DateFormatWithHypen) != "2026-06-10" {
		t.Fatalf("parsed date = %s", got.MustGet().Format(DateFormatWithHypen))
	}
}

func TestParseTimeInLocationValidation(t *testing.T) {
	loc := time.UTC
	tests := []struct {
		name     string
		format   string
		datetime string
		loc      *time.Location
	}{
		{name: "empty date", format: DateFormatWithHypen, loc: loc},
		{name: "empty location", format: DateFormatWithHypen, datetime: "2026-06-10"},
		{name: "invalid format", format: DateFormatWithHypen, datetime: "06/10/2026", loc: loc},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseTimeInLocation(tt.format, tt.datetime, tt.loc)
			if result.IsOk() {
				t.Fatalf("expected error, got %v", result.MustGet())
			}
			if result.Error() == nil {
				t.Fatal("expected error details")
			}
		})
	}
}

func TestParseBeijingDate(t *testing.T) {
	got := ParseBeijingDate("2026-06-10")
	if got.IsError() {
		t.Fatalf("ParseBeijingDate returned error: %v", got.Error())
	}
	if got.MustGet().Format(time.RFC3339) != "2026-06-10T00:00:00+08:00" {
		t.Fatalf("beijing date = %s", got.MustGet().Format(time.RFC3339))
	}
}
