package db

import (
	"context"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestOpen(t *testing.T) {
	store := openTestDB(t)
	if store == nil {
		t.Fatal("expected non-nil store")
	}
}

func TestLogAndRetrieveEvents(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	ev := PowerEvent{
		Action:     "power_on",
		UserLogin:  "cam@example.com",
		UserName:   "Cam",
		Success:    true,
		DurationMS: 250,
	}

	if err := store.LogEvent(ctx, ev); err != nil {
		t.Fatalf("LogEvent() error = %v", err)
	}

	events, err := store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents() error = %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}

	got := events[0]
	if got.Action != "power_on" {
		t.Errorf("Action = %q, want %q", got.Action, "power_on")
	}
	if got.UserLogin != "cam@example.com" {
		t.Errorf("UserLogin = %q, want %q", got.UserLogin, "cam@example.com")
	}
	if got.UserName != "Cam" {
		t.Errorf("UserName = %q, want %q", got.UserName, "Cam")
	}
	if !got.Success {
		t.Error("Success = false, want true")
	}
	if got.DurationMS != 250 {
		t.Errorf("DurationMS = %d, want 250", got.DurationMS)
	}
	if got.ID == 0 {
		t.Error("ID should be non-zero")
	}
	if got.Timestamp.IsZero() {
		t.Error("Timestamp should be non-zero")
	}
}

func TestLogEventWithError(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	ev := PowerEvent{
		Action:    "power_off",
		UserLogin: "test",
		Success:   false,
		ErrorMsg:  "BMC unreachable",
	}

	if err := store.LogEvent(ctx, ev); err != nil {
		t.Fatalf("LogEvent() error = %v", err)
	}

	events, err := store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents() error = %v", err)
	}

	if events[0].ErrorMsg != "BMC unreachable" {
		t.Errorf("ErrorMsg = %q, want %q", events[0].ErrorMsg, "BMC unreachable")
	}
}

func TestRecentEventsOrdering(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	actions := []string{"power_on", "power_off", "power_cycle"}
	for _, a := range actions {
		if err := store.LogEvent(ctx, PowerEvent{Action: a, UserLogin: "test"}); err != nil {
			t.Fatalf("LogEvent() error = %v", err)
		}
		// Small delay so timestamps differ
		time.Sleep(10 * time.Millisecond)
	}

	events, err := store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents() error = %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}

	// Most recent first
	if events[0].Action != "power_cycle" {
		t.Errorf("events[0].Action = %q, want %q", events[0].Action, "power_cycle")
	}
	if events[2].Action != "power_on" {
		t.Errorf("events[2].Action = %q, want %q", events[2].Action, "power_on")
	}
}

func TestRecentEventsLimit(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := store.LogEvent(ctx, PowerEvent{Action: "power_on", UserLogin: "test"}); err != nil {
			t.Fatalf("LogEvent() error = %v", err)
		}
	}

	events, err := store.RecentEvents(ctx, 2)
	if err != nil {
		t.Fatalf("RecentEvents() error = %v", err)
	}

	if len(events) != 2 {
		t.Errorf("got %d events, want 2", len(events))
	}
}

func TestRecentEventsEmpty(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	events, err := store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents() error = %v", err)
	}

	if len(events) != 0 {
		t.Errorf("got %d events, want 0", len(events))
	}
}
