package resource_test

import (
	"runtime"
	"testing"

	"github.com/reconcile-kit/api/resource"
)

func TestDeepCopyStructZeroSizePointersWithoutOwner(t *testing.T) {
	t.Parallel()
	// The zero-sized type still requires uint64 alignment. Its original
	// nonzero owners are retained by the pointers but are not in the copy graph.
	type zero [0]uint64
	type pointer *zero
	type slot struct {
		key zero
		pad byte
	}
	type object struct {
		index  map[*zero]int
		first  pointer
		repeat *zero
		second *zero
	}
	slots := new([2]slot)
	src := &object{
		index:  map[*zero]int{&slots[0].key: 1, &slots[1].key: 2},
		first:  &slots[0].key,
		repeat: &slots[0].key,
		second: &slots[1].key,
	}
	if len(src.index) != 2 {
		t.Fatal("fixture must have two distinct keys")
	}
	first := resource.DeepCopyStruct(src).(*object)
	second := resource.DeepCopyStruct(src).(*object)
	runtime.GC()
	for _, got := range []*object{first, second} {
		if len(got.index) != 2 || got.index[got.first] != 1 || got.index[got.second] != 2 {
			t.Fatal("zero-sized pointer keys lost their values or identities")
		}
		if got.first != got.repeat || got.first == got.second {
			t.Fatal("repeated and distinct zero-sized pointers were not preserved")
		}
		if got.first == src.first || got.second == src.second {
			t.Fatal("zero-sized pointer still refers to the source")
		}
	}
	if first.first == second.first || first.second == second.second {
		t.Fatal("independent calls share zero-sized pointer storage")
	}
}

func TestDeepCopyStructArraySliceCycles(t *testing.T) {
	t.Parallel()
	type item struct {
		data  []byte
		owner any
	}
	type view []item
	type object struct {
		elem  *item
		array [2]item
		view  view
		alias []item
	}
	src := &object{}
	src.elem = &src.array[1]
	src.view = src.array[:]
	src.alias = src.view
	for i := range src.array {
		src.array[i] = item{data: []byte{byte(i + 1)}, owner: src}
	}
	first := resource.DeepCopyStruct(src).(*object)
	second := resource.DeepCopyStruct(src).(*object)
	runtime.GC()
	for _, got := range []*object{first, second} {
		if got.elem != &got.array[1] || got.elem != &got.view[1] || got.elem != &got.alias[1] {
			t.Fatal("array, named slice, identical view and element pointer lost their shared storage")
		}
		if got.elem.owner != got || got.view[0].owner != got {
			t.Fatal("array/slice cycle points to the wrong owner")
		}
	}
	first.elem.data[0] = 9
	first.view[0] = item{data: []byte{8}, owner: first}
	if first.array[0].data[0] != 8 || first.view[1].data[0] != 9 {
		t.Fatal("array and slice mutations are not visible through their aliases")
	}
	for _, got := range []*object{src, second} {
		if got.array[0].data[0] != 1 || got.array[1].data[0] != 2 {
			t.Fatal("array/slice mutation escaped its copy")
		}
	}
}

func TestDeepCopyStructArrayNestedInSlice(t *testing.T) {
	t.Parallel()
	type item struct {
		array [2][3]int
		view  []int
	}
	type object struct {
		elem  *int
		items []item
	}
	src := &object{items: make([]item, 2)}
	src.items[1].array[1] = [3]int{1, 2, 3}
	src.items[1].view = src.items[1].array[1][1:]
	src.elem = &src.items[1].view[0]
	got := resource.DeepCopyStruct(src).(*object)
	if got.elem != &got.items[1].array[1][1] || got.elem != &got.items[1].view[0] {
		t.Fatal("slice did not use its nested array's copied storage")
	}
	*got.elem = 9
	if got.items[1].view[0] != 9 || src.items[1].view[0] != 2 {
		t.Fatal("nested array view lost aliasing or source isolation")
	}
}

func TestDeepCopyStructArrayViewSpareCapacity(t *testing.T) {
	tests := []struct {
		name   string
		length int
	}{
		{name: "empty", length: 0},
		{name: "nonempty", length: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			type object struct {
				array [3]int
				view  []int
				alias []int
			}
			src := &object{array: [3]int{1, 2, 3}}
			src.view = src.array[:tt.length]
			src.alias = src.view
			got := resource.DeepCopyStruct(src).(*object)
			if got.array != src.array || len(got.view) != tt.length || cap(got.view) != 3 {
				t.Fatal("array values or slice length/capacity changed")
			}
			for _, value := range got.view[tt.length:cap(got.view)] {
				if value != 0 {
					t.Fatal("array view copied elements beyond len")
				}
			}
			got.view = append(got.view, 9)
			if got.alias[:cap(got.alias)][tt.length] != 9 {
				t.Fatal("identical views lost their shared storage")
			}
			if got.array != src.array || src.array != [3]int{1, 2, 3} {
				t.Fatal("append changed the source or the copied owner's other elements")
			}
		})
	}
}

func TestDeepCopyStructSliceOwnsVisibleArray(t *testing.T) {
	t.Parallel()
	type object struct {
		array *[2]int
		view  []int
		elem  *int
	}
	backing := []int{1, 2, 3, 4}
	// The pointer and the visible slice have the same start and size. Visit
	// the pointer first to exercise ownership independent of discovery order.
	src := &object{array: (*[2]int)(backing), view: backing[:2], elem: &backing[1]}
	got := resource.DeepCopyStruct(src).(*object)
	if got.elem != &got.array[1] || got.elem != &got.view[1] {
		t.Fatal("array pointer and visible slice lost their shared storage")
	}
	if cap(got.view) != 4 || got.view[:4][2] != 0 || got.view[:4][3] != 0 {
		t.Fatal("slice spare capacity was not preserved and zeroed")
	}
	*got.elem = 9
	if got.view[1] != 9 || backing[1] != 2 {
		t.Fatal("slice owner lost aliasing or source isolation")
	}
}
