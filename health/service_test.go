package health

import "testing"

func TestReadyFlip(t *testing.T) {
	s := NewService()
	if s.Ready() {
		t.Fatal("Ready() should be false initially")
	}
	if !s.Live() {
		t.Fatal("Live() should always be true")
	}
	s.SetReady(true)
	if !s.Ready() {
		t.Fatal("Ready() should be true after SetReady(true)")
	}
	s.SetReady(false)
	if s.Ready() {
		t.Fatal("Ready() should be false after SetReady(false)")
	}
}
