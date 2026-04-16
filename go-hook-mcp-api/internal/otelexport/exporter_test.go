package otelexport

import (
	"testing"
)

func TestParseHeaders_Empty(t *testing.T) {
	h := ParseHeaders("")
	if len(h) != 0 {
		t.Errorf("len = %d; want 0", len(h))
	}
}

func TestParseHeaders_SinglePair(t *testing.T) {
	h := ParseHeaders("Authorization=Bearer token123")
	if h["Authorization"] != "Bearer token123" {
		t.Errorf("Authorization = %q; want %q", h["Authorization"], "Bearer token123")
	}
}

func TestParseHeaders_MultiplePairs(t *testing.T) {
	h := ParseHeaders("X-Key=val1,X-Other=val2")
	if h["X-Key"] != "val1" {
		t.Errorf("X-Key = %q; want %q", h["X-Key"], "val1")
	}
	if h["X-Other"] != "val2" {
		t.Errorf("X-Other = %q; want %q", h["X-Other"], "val2")
	}
}

func TestParseHeaders_Malformed(t *testing.T) {
	h := ParseHeaders("noequals,valid=ok")
	if len(h) != 1 {
		t.Errorf("len = %d; want 1", len(h))
	}
	if h["valid"] != "ok" {
		t.Errorf("valid = %q; want %q", h["valid"], "ok")
	}
}

func TestParseHeaders_WithSpaces(t *testing.T) {
	h := ParseHeaders(" key = value , key2 = value2 ")
	if h["key"] != "value" {
		t.Errorf("key = %q; want %q", h["key"], "value")
	}
	if h["key2"] != "value2" {
		t.Errorf("key2 = %q; want %q", h["key2"], "value2")
	}
}
