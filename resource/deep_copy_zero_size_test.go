package resource_test

import (
	"reflect"
	"runtime"
	"testing"

	"github.com/reconcile-kit/api/resource"
)

func TestDeepCopyStructZeroSizeSliceReferences(t *testing.T) {
	t.Run("struct", testZeroSizeSliceReferences[struct{}])
	t.Run("aligned_array", testZeroSizeSliceReferences[[0]uint64])
}

func testZeroSizeSliceReferences[T any](t *testing.T) {
	t.Helper()
	tests := []struct {
		name     string
		length   int
		capacity int
	}{
		{name: "full", length: 1, capacity: 1},
		{name: "spare_capacity", length: 1, capacity: 3},
		{name: "empty_with_capacity", length: 0, capacity: 3},
		{name: "multiple_elements", length: 2, capacity: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			type pointer *T
			type view []T
			type object struct {
				pointer pointer
				index   map[*T]int
				data    view
				alias   []T
				tight   []T
				value   any
				missing []T
				empty   []T
			}
			backing := make([]T, tt.capacity)
			src := &object{
				pointer: &backing[0],
				index:   map[*T]int{&backing[0]: 7},
				data:    backing[:tt.length],
				alias:   backing[:tt.length],
				tight:   backing[:1:1],
				value:   pointer(&backing[0]),
				empty:   []T{},
			}
			first := resource.DeepCopyStruct(src).(*object)
			second := resource.DeepCopyStruct(src).(*object)
			runtime.GC()
			for _, got := range []*object{first, second} {
				if len(got.data) != tt.length || cap(got.data) != tt.capacity {
					t.Fatal("zero-sized slice length or capacity changed")
				}
				if got.missing != nil || got.empty == nil || len(got.empty) != 0 || cap(got.empty) != 0 {
					t.Fatal("nil or empty zero-sized slice changed")
				}
				if got.pointer != got.value.(pointer) || got.pointer != &got.tight[0] {
					t.Fatal("named pointer, interface and slice view lost their shared address")
				}
				for i := range cap(got.data) {
					element := &got.data[:cap(got.data)][i]
					if element != got.pointer || element != &got.alias[:cap(got.alias)][i] {
						t.Fatal("zero-sized slice element lost its pointer or identical view")
					}
					if got.index[element] != 7 {
						t.Fatal("map lookup by the copied slice element failed")
					}
				}
				if len(got.index) != 1 || got.pointer == src.pointer {
					t.Fatal("pointer map changed cardinality or retained the source address")
				}
				if tt.length < tt.capacity {
					var value T
					got.data = append(got.data, value)
					if got.index[&got.data[tt.length]] != 7 {
						t.Fatal("append within capacity lost the element's map key")
					}
				}
			}
			if first.pointer == second.pointer {
				t.Fatal("independent copies share zero-sized storage")
			}
		})
	}
}

func TestDeepCopyStructDistinctZeroSizeSliceAddresses(t *testing.T) {
	tests := []struct {
		name         string
		includeOwner bool
	}{
		{name: "with_owners", includeOwner: true},
		{name: "without_owners"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			type zero [0]uint64
			type slot struct {
				array [3]zero
				pad   byte
			}
			type object struct {
				owners []*slot
				views  [][]zero
				index  map[*zero]int
			}
			slots := new([2]slot)
			src := &object{
				views: [][]zero{slots[0].array[:1], slots[1].array[:1]},
				index: map[*zero]int{&slots[0].array[0]: 1, &slots[1].array[0]: 2},
			}
			if tt.includeOwner {
				src.owners = []*slot{&slots[0], &slots[1]}
			}
			if len(src.index) != 2 {
				t.Fatal("fixture must have two distinct zero-sized addresses")
			}
			got := resource.DeepCopyStruct(src).(*object)
			runtime.GC()
			if len(got.index) != 2 {
				t.Fatal("distinct source addresses collapsed into one map key")
			}
			for i, view := range got.views {
				if len(view) != 1 || cap(view) != 3 || got.index[&view[0]] != i+1 {
					t.Fatal("zero-sized view lost its shape or map key")
				}
				if &view[0] == &src.views[i][0] {
					t.Fatal("zero-sized view still refers to the source")
				}
				if tt.includeOwner && &view[0] != &got.owners[i].array[0] {
					t.Fatal("zero-sized view lost its copied array owner")
				}
			}
		})
	}
}

func TestDeepCopyStructZeroSizeSharedAddressAlignment(t *testing.T) {
	t.Parallel()
	type zero struct{}
	type aligned [0]uint64
	type holder struct {
		low  [2]zero
		high [3]aligned
		pad  byte
	}
	type object struct {
		low         []zero
		high        []aligned
		lowPointer  *zero
		highPointer *aligned
		lowIndex    map[*zero]int
		highIndex   map[*aligned]int
	}
	storage := new(holder)
	src := &object{
		low:         storage.low[:1],
		high:        storage.high[:1],
		lowPointer:  &storage.low[0],
		highPointer: &storage.high[0],
		lowIndex:    map[*zero]int{&storage.low[0]: 1},
		highIndex:   map[*aligned]int{&storage.high[0]: 2},
	}
	original := reflect.ValueOf(src.lowPointer).Pointer()
	if original != reflect.ValueOf(src.highPointer).Pointer() {
		t.Fatal("fixture must share one address across zero-sized types")
	}
	got := resource.DeepCopyStruct(src).(*object)
	runtime.GC()
	low := reflect.ValueOf(got.lowPointer).Pointer()
	high := reflect.ValueOf(got.highPointer).Pointer()
	if low != high || low == original {
		t.Fatal("zero-sized types lost their shared address or retained the source address")
	}
	if high%uintptr(reflect.TypeFor[aligned]().Align()) != 0 {
		t.Fatal("shared zero-sized storage is not aligned for every reference")
	}
	if &got.low[0] != got.lowPointer || &got.high[0] != got.highPointer {
		t.Fatal("zero-sized slices no longer refer to their copied pointers")
	}
	if got.lowIndex[&got.low[0]] != 1 || got.highIndex[&got.high[0]] != 2 {
		t.Fatal("map lookup by a zero-sized element failed")
	}
	if len(got.low) != 1 || cap(got.low) != 2 || len(got.high) != 1 || cap(got.high) != 3 {
		t.Fatal("sharing zero-sized storage changed slice lengths or capacities")
	}
	runtime.KeepAlive(src)
}
