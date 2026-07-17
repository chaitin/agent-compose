package main

import "testing"

func TestFormatDaemonStatusTime(t *testing.T) {
	offset := 8 * 60 * 60
	if got := formatDaemonStatusTime(1783501631.2438176, "CST", &offset); got != "2026-07-08 17:07:11 CST +0800" {
		t.Fatalf("formatDaemonStatusTime() = %q, want server timezone time", got)
	}
	if got := formatDaemonStatusTime(1783501631.2438176, "", nil); got != "2026-07-08 09:07:11 UTC +0000" {
		t.Fatalf("formatDaemonStatusTime() = %q, want legacy UTC fallback time", got)
	}
}
