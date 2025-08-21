package main

import (
	"strings"
	"testing"
)

func TestCycleMode(t *testing.T) {
	m := initialModel()
	if m.currentMode != modeNucleotides {
		t.Fatalf("expected initial mode nucleotides, got %v", m.currentMode)
	}
	m = m.cycleMode()
	if m.currentMode != modeTranslated {
		t.Fatalf("expected translated, got %v", m.currentMode)
	}
	m = m.cycleMode()
	if m.currentMode != modeAlignment {
		t.Fatalf("expected alignment, got %v", m.currentMode)
	}
	m = m.cycleMode()
	if m.currentMode != modeNucleotides {
		t.Fatalf("expected nucleotides, got %v", m.currentMode)
	}
}

func TestBuildRightLinesWrap(t *testing.T) {
	m := initialModel()
	m.width = 120
	m.height = 40
	rec := DRD4Record{
		Name:             "test",
		VariantCode:      "V1",
		NucleotidesAlign: strings.Repeat("ATG", 50),
	}
	lines := m.buildRightLines(rec)
	if len(lines) == 0 {
		t.Fatalf("expected wrapped lines, got 0")
	}
}
