package resource_test

import (
	"reflect"
	"runtime"
	"sync/atomic"
	"testing"
	"weak"

	"github.com/reconcile-kit/api/resource"
)

func TestDeepCopyStructZeroArrayPointerInSpareCapacity(t *testing.T) {
	t.Run("byte", func(t *testing.T) { testZeroArrayPointerInSpareCapacity(t, byte(7), byte(9)) })
	t.Run("int", func(t *testing.T) { testZeroArrayPointerInSpareCapacity(t, 7, 9) })
	t.Run("pointer", func(t *testing.T) {
		before, after := 7, 9
		testZeroArrayPointerInSpareCapacity(t, &before, &after)
	})
}

func testZeroArrayPointerInSpareCapacity[T comparable](t *testing.T, before, after T) {
	t.Helper()
	tests := []struct {
		name   string
		length int
	}{
		{name: "empty"},
		{name: "inside_tail", length: 1},
		{name: "at_length", length: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			type pointer *[0]T
			type view []T
			type object struct {
				pointer pointer
				index   map[*[0]T]int
				value   any
				data    view
				alias   []T
			}
			backing := []T{before, before, before, before}
			p := (*[0]T)(backing[2:])
			src := &object{
				pointer: p,
				index:   map[*[0]T]int{p: 7, (*[0]T)(backing[3:]): 8},
				value:   pointer(p),
				data:    backing[:tt.length],
				alias:   backing[:tt.length],
			}
			first := resource.DeepCopyStruct(src).(*object)
			second := resource.DeepCopyStruct(src).(*object)
			runtime.GC()
			for _, got := range []*object{first, second} {
				if len(got.data) != tt.length || cap(got.data) != 4 {
					t.Fatal("slice length or capacity changed")
				}
				full := got.data[:cap(got.data)]
				fromSlice := (*[0]T)(full[2:])
				if got.pointer != fromSlice || got.value.(pointer) != fromSlice || (*[0]T)(got.alias[:4][2:]) != fromSlice {
					t.Fatal("zero-sized pointer in spare capacity lost the slice, identical view or interface")
				}
				if len(got.index) != 2 || got.index[fromSlice] != 7 || got.index[(*[0]T)(full[3:])] != 8 {
					t.Fatal("map lookup by a zero-sized pointer into spare capacity failed")
				}
				var zero T
				for _, value := range full[tt.length:] {
					if value != zero {
						t.Fatal("slice copied values beyond len")
					}
				}
			}
			if first.pointer == src.pointer || first.pointer == second.pointer || second.pointer == src.pointer {
				t.Fatal("independent copies share storage")
			}
			for len(first.data) < 3 {
				first.data = append(first.data, after)
			}
			if first.alias[:4][2] != after || first.index[(*[0]T)(first.data[2:])] != 7 {
				t.Fatal("append lost the identical view or pointer key")
			}
			var zero T
			if backing[2] != before || second.data[:4][2] != zero {
				t.Fatal("append changed the source or another copy")
			}
			owner := weak.Make(&first.data[:4][0])
			first.data, first.alias = nil, nil
			runtime.GC()
			if owner.Value() == nil {
				t.Fatal("GC reclaimed storage retained by zero-sized pointers into spare capacity")
			}
			runtime.KeepAlive(first)
		})
	}
}

func TestDeepCopyStructSpareCapacityZeroAlignment(t *testing.T) {
	tests := []struct {
		name string
		elem reflect.Type
	}{
		{name: "aligned_blocks", elem: reflect.TypeFor[byte]()},
		{name: "prefixed_storage", elem: reflect.TypeFor[[4]byte]()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			type zero [0]uint64
			type holder struct {
				prefix byte
				zero   zero
				data   [12]byte
			}
			type object struct {
				pointer *zero
				index   map[*zero]int
				view    any
			}
			storage := &holder{data: [12]byte{7}}
			// Start inside a pointer-free object, before its aligned zero-sized
			// field. The field is reachable only through the empty view's tail.
			bytes := reflect.SliceAt(reflect.TypeFor[byte](), reflect.ValueOf(storage).UnsafePointer(), int(reflect.TypeFor[holder]().Size())).Slice(1, 13)
			view := reflect.SliceAt(tt.elem, bytes.UnsafePointer(), 12/int(tt.elem.Size())).Slice(0, 0)
			src := &object{pointer: &storage.zero, index: map[*zero]int{&storage.zero: 7}, view: view.Interface()}
			offset := reflect.ValueOf(src.pointer).Pointer() - view.Pointer()
			got := resource.DeepCopyStruct(src).(*object)
			runtime.GC()
			copiedView := reflect.ValueOf(got.view)
			address := reflect.ValueOf(got.pointer).Pointer()
			if address%uintptr(reflect.TypeFor[zero]().Align()) != 0 || address-copiedView.Pointer() != offset {
				t.Fatal("zero-sized pointer in spare capacity lost alignment or its interior offset")
			}
			if copiedView.Len() != 0 || copiedView.Cap() != view.Cap() || got.index[got.pointer] != 7 || got.pointer == src.pointer {
				t.Fatal("empty view lost its shape, pointer key or isolation")
			}
			for _, value := range reflect.SliceAt(reflect.TypeFor[byte](), copiedView.UnsafePointer(), 12).Bytes() {
				if value != 0 {
					t.Fatal("aligned zero-sized reference populated the slice's unused tail")
				}
			}
			runtime.KeepAlive(storage)
		})
	}
}

func TestDeepCopyStructOverlappingArrayPointers(t *testing.T) {
	tests := []struct {
		name  string
		owner bool
		spare bool
	}{
		{name: "arrays"},
		{name: "containing_owner", owner: true},
		{name: "spare_slice", spare: true},
		{name: "owner_and_spare_slice", owner: true, spare: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			type pointer *[3]int
			type object struct {
				right  pointer
				shared *int
				index  map[*int]int
				last   *[2]int
				left   *[3]int
				owner  *[6]int
				value  any
				view   []int
			}
			backing := []int{1, 2, 3, 4, 5, 6}
			src := &object{
				right:  (*[3]int)(backing[2:]),
				shared: &backing[2],
				index:  map[*int]int{&backing[2]: 7, &backing[4]: 8},
				last:   (*[2]int)(backing[4:]),
				left:   (*[3]int)(backing),
				value:  pointer((*[3]int)(backing[2:])),
			}
			if tt.owner {
				src.owner = (*[6]int)(backing)
			}
			if tt.spare {
				src.view = backing[:3]
			}
			first := resource.DeepCopyStruct(src).(*object)
			second := resource.DeepCopyStruct(src).(*object)
			runtime.GC()
			for _, got := range []*object{first, second} {
				if got.shared != &got.left[2] || got.shared != &got.right[0] || &got.right[2] != &got.last[0] || got.value.(pointer) != got.right {
					t.Fatal("overlapping arrays, element pointer or named pointer lost their shared storage")
				}
				if got.index[&got.left[2]] != 7 || got.index[&got.last[0]] != 8 {
					t.Fatal("map lookup by an overlapping array element failed")
				}
				if *got.left != [3]int{1, 2, 3} || *got.right != [3]int{3, 4, 5} || *got.last != [2]int{5, 6} {
					t.Fatal("overlapping array contents changed")
				}
				if tt.owner && (&got.owner[2] != got.shared || &got.owner[4] != &got.last[0]) {
					t.Fatal("overlapping arrays lost their containing owner")
				}
				if tt.spare {
					if len(got.view) != 3 || cap(got.view) != 6 {
						t.Fatal("slice length or capacity changed")
					}
					for _, value := range got.view[3:6] {
						if value != 0 {
							t.Fatal("array union populated the slice's unused tail")
						}
					}
				}
			}
			*first.shared = 9
			first.last[0] = 10
			if first.left[2] != 9 || first.right[0] != 9 || first.right[2] != 10 {
				t.Fatal("mutation is not visible through overlapping arrays")
			}
			if backing[2] != 3 || backing[4] != 5 || second.left[2] != 3 || second.right[2] != 5 {
				t.Fatal("array mutation escaped its copy")
			}
		})
	}
}

func TestDeepCopyStructOverlappingNestedArraysGC(t *testing.T) {
	t.Parallel()
	type payload [128]byte
	type item struct {
		data  []byte
		value *payload
		next  *item
	}
	type object struct {
		right *[3][2]item
		flat  *[2]item
		left  *[3][2]item
		index map[*item]int
	}
	backing := make([][2]item, 4)
	for i := range 8 {
		backing[i/2][i%2] = item{
			data:  []byte{byte(i)},
			value: &payload{byte(i)},
			next:  &backing[(i+1)%8/2][(i+1)%2],
		}
	}
	src := &object{
		right: (*[3][2]item)(backing[1:]),
		flat:  &backing[2],
		left:  (*[3][2]item)(backing),
		index: map[*item]int{&backing[2][1]: 7},
	}
	first := resource.DeepCopyStruct(src).(*object)
	second := resource.DeepCopyStruct(src).(*object)
	retained := weak.Make(first.right[2][1].value)
	runtime.GC()
	if retained.Value() == nil {
		t.Fatal("GC lost a payload reachable through merged array storage")
	}
	for _, got := range []*object{first, second} {
		if &got.left[1][0] != &got.right[0][0] || &got.left[2] != got.flat || &got.right[1] != got.flat || got.index[&got.flat[1]] != 7 {
			t.Fatal("nested overlapping arrays lost their pointers or map key")
		}
		current := &got.left[0][0]
		for i := range 8 {
			if current == &backing[i/2][i%2] || current.value == backing[i/2][i%2].value || current.data[0] != byte(i) || current.value[0] != byte(i) {
				t.Fatal("array cycle retained source storage or lost contents")
			}
			current = current.next
		}
		if current != &got.left[0][0] {
			t.Fatal("array cycle does not return to the copied owner")
		}
	}
	first.flat[1].data[0] = 99
	first.flat[1].value[0] = 98
	if first.left[2][1].data[0] != 99 || first.right[1][1].value[0] != 98 || backing[2][1].data[0] != 5 || second.flat[1].value[0] != 5 {
		t.Fatal("nested array mutation lost aliasing or escaped its copy")
	}
	runtime.KeepAlive(first)
}

func TestDeepCopyStructOverlappingArrayAlignment(t *testing.T) {
	t.Parallel()
	type payload [128]byte
	type zero [0]atomic.Int64
	type holder struct {
		prefix int32
		first  *payload
		zero   zero
		rest   [3]*payload
	}
	type object struct {
		left  *[3]*payload
		right *[3]*payload
		zero  *zero
		index map[*zero]int
	}
	storage := &holder{first: &payload{1}, rest: [3]*payload{{2}, {3}, {4}}}
	// On 386 the pointers need four-byte alignment and the zero-sized alias
	// needs eight. The union's base starts four bytes before that alias.
	view := reflect.SliceAt(reflect.TypeFor[*payload](), reflect.ValueOf(&storage.first).UnsafePointer(), 4).Interface().([]*payload)
	src := &object{
		left: (*[3]*payload)(view), right: (*[3]*payload)(view[1:]),
		zero: &storage.zero, index: map[*zero]int{&storage.zero: 7},
	}
	got := resource.DeepCopyStruct(src).(*object)
	retained := weak.Make(got.right[2])
	runtime.GC()
	if retained.Value() == nil {
		t.Fatal("GC lost a payload in aligned array union storage")
	}
	if &got.left[1] != &got.right[0] || &got.left[2] != &got.right[1] {
		t.Fatal("aligned arrays lost their shared storage")
	}
	address := reflect.ValueOf(got.zero).Pointer()
	if address%uintptr(reflect.TypeFor[zero]().Align()) != 0 || address != reflect.ValueOf(&got.right[0]).Pointer() || got.index[got.zero] != 7 {
		t.Fatal("array union lost zero-sized alias alignment or identity")
	}
	for i, p := range []*payload{got.left[0], got.right[0], got.right[1], got.right[2]} {
		if p == view[i] || p[0] != byte(i+1) {
			t.Fatal("aligned array union lost values or source isolation")
		}
	}
	runtime.KeepAlive(got)
	runtime.KeepAlive(storage)
}

func TestDeepCopyStructOverlappingArrayTags(t *testing.T) {
	t.Parallel()
	type leftItem = struct {
		value *int `copy:"left"`
	}
	type rightItem = struct {
		value *int `copy:"right"`
	}
	type object struct {
		left  *[2]leftItem
		right *[2]rightItem
		index map[*leftItem]int
	}
	one, two, three := 1, 2, 3
	backing := []leftItem{{&one}, {&two}, {&three}}
	src := &object{
		left:  (*[2]leftItem)(backing),
		right: (*[2]rightItem)((*[2]leftItem)(backing[1:])),
		index: map[*leftItem]int{&backing[1]: 7},
	}
	got := resource.DeepCopyStruct(src).(*object)
	if &got.left[1] != (*leftItem)(&got.right[0]) || got.index[(*leftItem)(&got.right[0])] != 7 {
		t.Fatal("array pointers with different struct tags lost their shared storage or map key")
	}
	if got.left[1].value == &two || *got.right[1].value != 3 {
		t.Fatal("array pointer conversion lost values or source isolation")
	}
	nine := 9
	got.right[0].value = &nine
	if got.left[1].value != &nine || *src.right[0].value != 2 {
		t.Fatal("converted array mutation lost aliasing or changed the source")
	}
}
