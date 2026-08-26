package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

var ErrNotFound = errors.New("event not found")

type Event struct {
	ID           string              `json:"id"`
	Channel      string              `json:"channel"`
	Method       string              `json:"method"`
	Path         string              `json:"path"`
	Query        string              `json:"query,omitempty"`
	RemoteAddr   string              `json:"remoteAddress"`
	ReceivedAt   time.Time           `json:"receivedAt"`
	Headers      map[string][]string `json:"headers"`
	Body         string              `json:"body"`
	BodyEncoding string              `json:"bodyEncoding"`
	ContentType  string              `json:"contentType,omitempty"`
	Size         int64               `json:"size"`
	Truncated    bool                `json:"truncated"`
}

type Summary struct {
	ID          string    `json:"id"`
	Channel     string    `json:"channel"`
	Method      string    `json:"method"`
	ReceivedAt  time.Time `json:"receivedAt"`
	ContentType string    `json:"contentType,omitempty"`
	Size        int64     `json:"size"`
	Truncated   bool      `json:"truncated"`
	Preview     string    `json:"preview"`
}

type Store struct {
	mu     sync.RWMutex
	path   string
	limit  int
	events []Event
}

func New(path string, limit int) (*Store, error) {
	if limit < 1 {
		return nil, errors.New("retention must be at least 1")
	}
	s := &Store{path: path, limit: limit}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.events); err != nil {
		return nil, err
	}
	if len(s.events) > limit {
		s.events = s.events[:limit]
	}
	return s, nil
}

func (s *Store) Add(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append([]Event{cloneEvent(event)}, s.events...)
	if len(s.events) > s.limit {
		s.events = s.events[:s.limit]
	}
	return s.persistLocked()
}

func (s *Store) List(channel string) []Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Summary, 0, len(s.events))
	for _, event := range s.events {
		if channel != "" && event.Channel != channel {
			continue
		}
		preview := event.Body
		if len(preview) > 140 {
			preview = preview[:140] + "…"
		}
		result = append(result, Summary{
			ID: event.ID, Channel: event.Channel, Method: event.Method,
			ReceivedAt: event.ReceivedAt, ContentType: event.ContentType,
			Size: event.Size, Truncated: event.Truncated, Preview: preview,
		})
	}
	return result
}

func (s *Store) Get(id string) (Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, event := range s.events {
		if event.ID == id {
			return cloneEvent(event), nil
		}
	}
	return Event{}, ErrNotFound
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, event := range s.events {
		if event.ID == id {
			s.events = slices.Delete(s.events, index, index+1)
			return s.persistLocked()
		}
	}
	return ErrNotFound
}

func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = nil
	return s.persistLocked()
}

func (s *Store) persistLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.events, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".webhook-workbench-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, s.path); err == nil {
		return nil
	}
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tempName, s.path)
}

func cloneEvent(event Event) Event {
	clone := event
	clone.Headers = make(map[string][]string, len(event.Headers))
	for key, values := range event.Headers {
		clone.Headers[key] = slices.Clone(values)
	}
	return clone
}
