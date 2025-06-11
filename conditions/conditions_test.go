package conditions

import (
	"testing"
	"time"
)

// stub implements ConditionsAccessor for testing purposes.
type stub struct {
	conds []Condition
}

func (s *stub) GetConditions() []Condition  { return s.conds }
func (s *stub) SetConditions(c []Condition) { s.conds = c }

func TestMarkHelpersAndSorting(t *testing.T) {
	obj := &stub{}

	MarkTrue(obj, Type("B"))
	MarkTrue(obj, Type("A"))
	MarkUnknown(obj, Type("C"), "", "")

	got := obj.GetConditions()
	if len(got) != 3 {
		t.Fatalf("expected 3 conditions, got %d", len(got))
	}

	// Expected order: A, B, C (lexicographic)
	wantOrder := []Type{"A", "B", "C"}
	for i, w := range wantOrder {
		if got[i].Type != w {
			t.Fatalf("unexpected order: index %d want %s got %s", i, w, got[i].Type)
		}
	}
}

func TestSyncReadyAllTrue(t *testing.T) {
	obj := &stub{}
	MarkTrue(obj, Type("Database"))
	MarkTrue(obj, Type("API"))

	SyncReady(obj)

	conds := obj.GetConditions()
	if !IsTrue(obj, Ready) {
		t.Fatalf("Ready should be true when all other conditions are true")
	}

	// Ready must be first.
	if conds[0].Type != Ready {
		t.Fatalf("Ready condition is not first")
	}
}

func TestSyncReadyWithFalse(t *testing.T) {
	obj := &stub{}
	MarkTrue(obj, Type("API"))
	MarkFalse(obj, Type("Database"), "DBDown", "database unreachable")

	SyncReady(obj)

	if IsTrue(obj, Ready) {
		t.Fatalf("Ready should be false when a condition is false")
	}

	ready := get(&obj.conds, Ready)
	if ready.Reason != "DBDown" || ready.Message != "database unreachable" {
		t.Fatalf("Ready did not inherit reason/message from failing condition")
	}
}

func TestLastTransitionTimeUpdate(t *testing.T) {
	obj := &stub{}
	MarkTrue(obj, Type("Cache"))
	cond := get(&obj.conds, Type("Cache"))
	firstTime := cond.LastTransitionTime

	// ensure non-zero
	if firstTime.IsZero() {
		t.Fatalf("LastTransitionTime not set on first mark")
	}

	time.Sleep(1 * time.Millisecond)
	MarkFalse(obj, Type("Cache"), "CacheDown", "cache offline")
	cond = get(&obj.conds, Type("Cache"))
	if !cond.LastTransitionTime.After(firstTime) {
		t.Fatalf("LastTransitionTime not updated on status change")
	}
}
