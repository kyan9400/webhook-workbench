package store

import (
	"path/filepath"
	"testing"
	"time"
)

func event(id, channel string) Event {
	return Event{ID: id, Channel: channel, ReceivedAt: time.Now(), Headers: map[string][]string{"X-Test": {"yes"}}, Body: id}
}

func TestStoreRetentionAndFiltering(t *testing.T) {
	s, err := New("", 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []Event{event("one", "orders"), event("two", "billing"), event("three", "orders")} {
		if err := s.Add(item); err != nil {
			t.Fatal(err)
		}
	}
	all := s.List("")
	if len(all) != 2 || all[0].ID != "three" || all[1].ID != "two" {
		t.Fatalf("unexpected list: %#v", all)
	}
	orders := s.List("orders")
	if len(orders) != 1 || orders[0].ID != "three" {
		t.Fatalf("unexpected filter: %#v", orders)
	}
}

func TestStorePersistsAndCopies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	s, err := New(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	item := event("one", "orders")
	if err := s.Add(item); err != nil {
		t.Fatal(err)
	}
	item.Headers["X-Test"][0] = "changed"

	reloaded, err := New(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.Get("one")
	if err != nil {
		t.Fatal(err)
	}
	if got.Headers["X-Test"][0] != "yes" {
		t.Fatalf("stored event was mutated: %#v", got)
	}
	got.Headers["X-Test"][0] = "again"
	again, _ := reloaded.Get("one")
	if again.Headers["X-Test"][0] != "yes" {
		t.Fatal("Get returned shared header storage")
	}
}

func TestDeleteAndClear(t *testing.T) {
	s, _ := New("", 10)
	_ = s.Add(event("one", "orders"))
	_ = s.Add(event("two", "orders"))
	if err := s.Delete("one"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("one"); err != ErrNotFound {
		t.Fatalf("expected not found, got %v", err)
	}
	if err := s.Delete("missing"); err != ErrNotFound {
		t.Fatalf("expected not found, got %v", err)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if len(s.List("")) != 0 {
		t.Fatal("store was not cleared")
	}
}

func TestInvalidRetention(t *testing.T) {
	if _, err := New("", 0); err == nil {
		t.Fatal("expected error")
	}
}
