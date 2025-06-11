package conditions

// Package conditions provides a minimal, dependency‑free helper set for
// managing Kubernetes‑style Conditions.
//
// Key points:
//   • Conditions are kept lexicographically sorted **inside set** – right
//     when a condition is created or updated.
//   • Public helpers (MarkTrue / MarkFalse / MarkUnknown / SyncReady)
//   • Resources integrate by implementing the ConditionsAccessor interface.

import (
	"sort"
	"time"
)

// -----------------------------------------------------------------------------
// Types and core structure
// -----------------------------------------------------------------------------

type Type string

// Ready is the aggregate condition name.
const Ready Type = "Ready"

// ConditionStatus mirrors the semantics of metav1.ConditionStatus but without
// importing apimachinery.
type ConditionStatus string

const (
	True    ConditionStatus = "True"
	False   ConditionStatus = "False"
	Unknown ConditionStatus = "Unknown"
)

// Condition represents a single entry in the status.conditions array.
// LastTransitionTime is stored as time.Time (UTC).
// All json tags follow Kubernetes conventions so the struct can be embedded in
// CRD status sections.
type Condition struct {
	Type               Type            `json:"type"`
	Status             ConditionStatus `json:"status"`
	Reason             string          `json:"reason,omitempty"`
	Message            string          `json:"message,omitempty"`
	LastTransitionTime time.Time       `json:"lastTransitionTime,omitempty"`
}

// -----------------------------------------------------------------------------
// Accessor interface – embed this in your Status struct
// -----------------------------------------------------------------------------

// ConditionsAccessor lets the helper functions read/write the slice without
// knowing the surrounding struct layout.
// Typical implementation:
//
//	func (s *MyStatus) GetConditions() []conditions.Condition { return s.Conditions }
//	func (s *MyStatus) SetConditions(c []conditions.Condition) { s.Conditions = c }
type ConditionsAccessor interface {
	GetConditions() []Condition
	SetConditions([]Condition)
}

// -----------------------------------------------------------------------------
// Internal helpers
// -----------------------------------------------------------------------------

func lexicographicLess(a, b *Condition) bool {
	// Ready always comes first. All other conditions are compared by Type.
	if a.Type == Ready && b.Type != Ready {
		return true
	}
	if b.Type == Ready && a.Type != Ready {
		return false
	}
	return string(a.Type) < string(b.Type)
}
func sortConditions(conds []Condition) {
	sort.Slice(conds, func(i, j int) bool {
		return lexicographicLess(&conds[i], &conds[j])
	})
}

// get returns a pointer to the condition of the requested type and its index.
func get(conds *[]Condition, t Type) *Condition {
	for i := range *conds {
		if (*conds)[i].Type == t {
			return &(*conds)[i]
		}
	}
	return nil
}

// set creates or updates a condition, then keeps the slice sorted.
func set(conds *[]Condition, t Type, s ConditionStatus, reason, msg string) {
	now := time.Now().UTC()

	if cond := get(conds, t); cond != nil {
		if cond.Reason != reason {
			cond.LastTransitionTime = now
		}
		cond.Status = s
		cond.Reason = reason
		cond.Message = msg
	} else {
		*conds = append(*conds, Condition{
			Type:               t,
			Status:             s,
			Reason:             reason,
			Message:            msg,
			LastTransitionTime: now,
		})
	}

	// Maintain lexicographic order after every mutation.
	sortConditions(*conds)
}

// -----------------------------------------------------------------------------
// Public helper functions
// -----------------------------------------------------------------------------

// MarkTrue sets the given condition to True.
func MarkTrue(obj ConditionsAccessor, t Type) {
	conds := append([]Condition(nil), obj.GetConditions()...) // copy for safety
	set(&conds, t, True, "", "")
	obj.SetConditions(conds)
}

// MarkFalse sets the given condition to False.
func MarkFalse(obj ConditionsAccessor, t Type, reason, msg string) {
	conds := append([]Condition(nil), obj.GetConditions()...)
	set(&conds, t, False, reason, msg)
	obj.SetConditions(conds)
}

// MarkUnknown sets the given condition to Unknown.
func MarkUnknown(obj ConditionsAccessor, t Type, reason, msg string) {
	conds := append([]Condition(nil), obj.GetConditions()...)
	set(&conds, t, Unknown, reason, msg)
	obj.SetConditions(conds)
}

// IsTrue reports whether a condition of the given type is currently True.
func IsTrue(obj ConditionsAccessor, t Type) bool {
	conds := obj.GetConditions()
	c := get(&conds, t)
	return c != nil && c.Status == True
}

// -----------------------------------------------------------------------------
// SyncReady – compute and update the aggregate Ready condition
// -----------------------------------------------------------------------------

// SyncReady recomputes the Ready condition based on all other conditions:
//   - Ready = False if at least one non‑Ready condition is False (Reason/Message
//     are copied from the first False condition encountered).
//   - Otherwise Ready = True.
//
// Unknown conditions are ignored – they neither set Ready=False nor True.
func SyncReady(obj ConditionsAccessor) {
	conds := append([]Condition(nil), obj.GetConditions()...)

	var (
		foundFalse   bool
		falseReason  string
		falseMessage string
	)

	for _, c := range conds {
		if c.Type == Ready {
			continue
		}
		if c.Status == False {
			foundFalse = true
			falseReason = c.Reason
			falseMessage = c.Message
			break
		}
	}

	if foundFalse {
		set(&conds, Ready, False, falseReason, falseMessage)
	} else {
		set(&conds, Ready, True, "", "")
	}

	obj.SetConditions(conds)
}
