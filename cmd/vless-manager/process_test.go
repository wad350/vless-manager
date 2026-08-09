package main

import (
	"strings"
	"testing"
)

func TestServiceLogLevelFiltering(t *testing.T) {
	logs := newRingBuffer()
	logs.setLevel("warn")
	logs.log(serviceLogInfo, "hidden")
	logs.log(serviceLogWarn, "visible warning")
	logs.log(serviceLogError, "visible error")

	lines, _ := logs.Lines(0)
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "level=WARN") || !strings.Contains(lines[1], "level=ERROR") {
		t.Fatalf("unexpected levels: %v", lines)
	}
}

func TestClearLogsDropsBufferedEntries(t *testing.T) {
	logs := newRingBuffer()
	logs.log(serviceLogInfo, "old entry")
	logs.clear()

	entries, seq := logs.Entries(0)
	if len(entries) != 0 {
		t.Fatalf("entries after clear: %+v", entries)
	}
	if seq != 1 {
		t.Fatalf("clear must preserve sequence, got %d", seq)
	}

	logs.log(serviceLogError, "new entry")
	entries, seq = logs.Entries(1)
	if seq != 2 || len(entries) != 1 || entries[0].Level != "ERROR" {
		t.Fatalf("entries after new log: seq=%d entries=%+v", seq, entries)
	}
}

func TestStructuredServiceLogEntry(t *testing.T) {
	logs := newRingBuffer()
	logs.logEvent(serviceLogInfo, "bypass", "refresh.succeeded", "updated",
		field("domains", 910), field("transport", "wan"))

	entries, seq := logs.Entries(0)
	if seq != 1 || len(entries) != 1 {
		t.Fatalf("seq=%d entries=%v", seq, entries)
	}
	entry := entries[0]
	if entry.Component != "bypass" || entry.Event != "refresh.succeeded" ||
		entry.Fields["domains"] != "910" || entry.Fields["transport"] != "wan" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}
