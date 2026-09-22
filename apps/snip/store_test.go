package main

import (
	"errors"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateAndGetLink(t *testing.T) {
	s := newTestStore(t)

	code, err := s.CreateLink("https://example.com")
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	if code == "" {
		t.Fatal("expected non-empty code")
	}

	link, err := s.GetLink(code)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if link.URL != "https://example.com" {
		t.Errorf("URL = %q, want %q", link.URL, "https://example.com")
	}
	if link.Clicks != 0 {
		t.Errorf("Clicks = %d, want 0", link.Clicks)
	}
}

func TestGetLink_NotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetLink("doesnotexist")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestIncrementClicks(t *testing.T) {
	s := newTestStore(t)

	code, _ := s.CreateLink("https://example.com")

	if err := s.IncrementClicks(code); err != nil {
		t.Fatalf("IncrementClicks: %v", err)
	}
	if err := s.IncrementClicks(code); err != nil {
		t.Fatalf("IncrementClicks: %v", err)
	}

	link, err := s.GetLink(code)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if link.Clicks != 2 {
		t.Errorf("Clicks = %d, want 2", link.Clicks)
	}
}

func TestDifferentURLsGetDifferentCodes(t *testing.T) {
	s := newTestStore(t)

	code1, _ := s.CreateLink("https://example.com/one")
	code2, _ := s.CreateLink("https://example.com/two")
	if code1 == code2 {
		t.Errorf("expected different codes, got %q twice", code1)
	}
}
