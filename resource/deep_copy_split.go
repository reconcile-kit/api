package resource

import (
	"reflect"
	"unsafe"
)

type copySlotMode uint8

const (
	copySlotOpaque copySlotMode = 1 << iota
	copySlotDeep
)

// Only type metadata enters copyPlans. Modes describe the pointer words in a
// value itself, not the contents of an object reached through those pointers.
func copySlotModesFor(typ reflect.Type) copySlotMode {
	if typ == copyTimeType {
		return copySlotOpaque
	}
	switch typ.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		return copySlotDeep
	case reflect.Interface:
		return copySlotOpaque | copySlotDeep
	case reflect.Chan, reflect.Func, reflect.String, reflect.UnsafePointer:
		return copySlotOpaque
	case reflect.Array:
		if typ.Len() != 0 {
			return planFor(typ.Elem()).slotModes
		}
	case reflect.Struct:
		var modes copySlotMode
		for i := range typ.NumField() {
			modes |= planFor(typ.Field(i).Type).slotModes
		}
		return modes
	}
	return 0
}

// An unsafe view can retain a pointer word that another view must rewrite.
// Color the conflicting allocations before selecting any containing owners.
// Compatible allocations prefer the first group available; each reference is
// still copied once, even when several detached owners could contain it.
func partitionCopyAllocations(allocations []copyAllocation) [][]copyAllocation {
	active := make([]int, 0, 8)
	var conflicts [][]int
	for i, current := range allocations {
		if current.size == 0 || planFor(current.reference.typ.Elem()).slotModes == 0 {
			continue
		}
		start := uintptr(current.reference.pointer)
		kept := active[:0]
		for _, j := range active {
			previous := allocations[j]
			if uintptr(previous.reference.pointer)+previous.size <= start {
				continue
			}
			kept = append(kept, j)
			if !copySlotsConflict(previous, current) {
				continue
			}
			if conflicts == nil {
				conflicts = make([][]int, len(allocations))
			}
			conflicts[i] = append(conflicts[i], j)
		}
		active = append(kept, i)
	}
	if conflicts == nil {
		return nil
	}
	colors := make([]int, len(allocations))
	var groups [][]copyAllocation
	for i, allocation := range allocations {
		blocked := make([]bool, len(groups))
		for _, previous := range conflicts[i] {
			blocked[colors[previous]] = true
		}
		color := 0
		for color < len(blocked) && blocked[color] {
			color++
		}
		if color == len(groups) {
			groups = append(groups, nil)
		}
		colors[i] = color
		groups[color] = append(groups[color], allocation)
	}
	return groups
}

func copySlotsConflict(left, right copyAllocation) bool {
	a, b := left.reference, right.reference
	at, bt := a.typ.Elem(), b.typ.Elem()
	am, bm := planFor(at).slotModes, planFor(bt).slotModes
	if am&copySlotDeep == 0 && bm&copySlotDeep == 0 || am&copySlotOpaque == 0 && bm&copySlotOpaque == 0 {
		return false
	}
	base, other := uintptr(a.pointer), uintptr(b.pointer)
	if at == bt && (other-base)%at.Size() == 0 {
		return false
	}
	low := max(base, other)
	high := min(base+left.size, other+right.size)
	first, last := (low-base)/at.Size(), (high-base-1)/at.Size()
	for i := first; i <= last; i++ {
		compatible := walkCopySlots(at, base+i*at.Size(), low, high, false, func(address uintptr, mode copySlotMode) bool {
			offset := address - other
			if b.typ.Kind() == reflect.Slice {
				offset %= bt.Size()
			}
			rightMode := copySlotModeAt(bt, offset, false)
			return rightMode == 0 || mode == rightMode
		})
		if !compatible {
			return true
		}
	}
	return false
}

// Visit only reference words intersecting the query. Large arrays need no
// per-element metadata and a pointer to one field scans only that field's range.
func walkCopySlots(typ reflect.Type, base, low, high uintptr, opaque bool, visit func(uintptr, copySlotMode) bool) bool {
	if base >= high || base+typ.Size() <= low || planFor(typ).slotModes == 0 {
		return true
	}
	opaque = opaque || typ == copyTimeType
	switch typ.Kind() {
	case reflect.Struct:
		for i := range typ.NumField() {
			field := typ.Field(i)
			if !walkCopySlots(field.Type, base+field.Offset, low, high, opaque, visit) {
				return false
			}
		}
	case reflect.Array:
		size := typ.Elem().Size()
		first := (max(base, low) - base) / size
		last := (min(base+typ.Size(), high) - base - 1) / size
		for i := first; i <= last; i++ {
			if !walkCopySlots(typ.Elem(), base+i*size, low, high, opaque, visit) {
				return false
			}
		}
	default:
		for offset := uintptr(0); offset < typ.Size(); offset += unsafe.Sizeof(uintptr(0)) {
			address := base + offset
			if address < low || address >= high {
				continue
			}
			if mode := copySlotModeAt(typ, offset, opaque); mode != 0 && !visit(address, mode) {
				return false
			}
		}
	}
	return true
}

func copySlotModeAt(typ reflect.Type, offset uintptr, opaque bool) copySlotMode {
	opaque = opaque || typ == copyTimeType
	switch typ.Kind() {
	case reflect.Struct:
		for i := range typ.NumField() {
			field := typ.Field(i)
			if offset >= field.Offset && offset-field.Offset < field.Type.Size() {
				return copySlotModeAt(field.Type, offset-field.Offset, opaque)
			}
		}
		return 0
	case reflect.Array:
		return copySlotModeAt(typ.Elem(), offset%typ.Elem().Size(), opaque)
	case reflect.Interface:
		if offset == 0 {
			return copySlotOpaque
		}
		if offset == unsafe.Sizeof(uintptr(0)) {
			if opaque {
				return copySlotOpaque
			}
			return copySlotDeep
		}
		return 0
	}
	if offset != 0 {
		return 0
	}
	mode := planFor(typ).slotModes
	if opaque && mode != 0 {
		return copySlotOpaque
	}
	return mode
}
