package resource_test

import (
	"reflect"
	"runtime"
	"testing"
	"weak"

	"github.com/reconcile-kit/api/resource"
)

func TestDeepCopyStructEmptySliceArrayPointer(t *testing.T) {
	t.Run("byte", func(t *testing.T) { testEmptySliceArrayPointer(t, byte(7), byte(9)) })
	t.Run("int", func(t *testing.T) { testEmptySliceArrayPointer(t, 7, 9) })
	t.Run("pointer", func(t *testing.T) {
		before, after := 7, 9
		testEmptySliceArrayPointer(t, &before, &after)
	})
}

func testEmptySliceArrayPointer[T comparable](t *testing.T, before, after T) {
	t.Helper()
	tests := []struct {
		name     string
		capacity int
	}{
		{name: "zero_capacity"},
		{name: "spare_capacity", capacity: 3},
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
				missing []T
			}
			backing := make([]T, tt.capacity)
			for i := range backing {
				backing[i] = before
			}
			data := backing[:0]
			p := (*[0]T)(data)
			src := &object{
				pointer: p,
				index:   map[*[0]T]int{p: 7},
				value:   pointer(p),
				data:    data,
				alias:   data,
			}
			first := resource.DeepCopyStruct(src).(*object)
			second := resource.DeepCopyStruct(src).(*object)
			runtime.GC()
			for _, got := range []*object{first, second} {
				if got.data == nil || len(got.data) != 0 || cap(got.data) != tt.capacity || got.missing != nil {
					t.Fatal("empty or nil slice shape changed")
				}
				fromSlice := (*[0]T)(got.data)
				if got.pointer != fromSlice || (*[0]T)(got.alias) != fromSlice || got.value.(pointer) != fromSlice {
					t.Fatal("empty slice, identical view, named pointer and interface lost their shared address")
				}
				if got.index[fromSlice] != 7 || len(got.index) != 1 {
					t.Fatal("map lookup by a pointer converted from the copied empty slice failed")
				}
				if got.pointer == src.pointer {
					t.Fatal("empty slice retained its source address")
				}
				var zero T
				for _, value := range got.data[:cap(got.data)] {
					if value != zero {
						t.Fatal("empty slice copied values beyond len")
					}
				}
			}
			if first.pointer == second.pointer {
				t.Fatal("independent calls share empty slice storage")
			}
			if tt.capacity > 0 {
				first.data = append(first.data, after)
				if first.alias[:1][0] != after || first.index[(*[0]T)(first.data)] != 7 {
					t.Fatal("append within capacity lost the identical view or pointer key")
				}
				var zero T
				if backing[0] != before || second.data[:1][0] != zero {
					t.Fatal("append changed the source or another copy")
				}
			}
		})
	}
}

func TestDeepCopyStructZeroSizeByteOwnerAlignment(t *testing.T) {
	tests := []struct {
		name   string
		length int
	}{
		{name: "pointer", length: 1},
		{name: "array", length: 3},
		{name: "full_slice", length: 3},
		{name: "spare_slice", length: 1},
		{name: "empty_slice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			type low struct{}
			type high [0]uint64
			type holder struct {
				low   [2]low
				high  [2]high
				bytes [3]byte
			}
			type object struct {
				lowPointer  *low
				highPointer *high
				index       map[*high]int
				view        []high
				owner       any
			}
			storage := &holder{bytes: [3]byte{7, 8, 9}}
			src := &object{
				lowPointer:  &storage.low[0],
				highPointer: &storage.high[0],
				index:       map[*high]int{&storage.high[0]: 7},
				view:        storage.high[:0],
			}
			switch tt.name {
			case "pointer":
				src.owner = &storage.bytes[0]
			case "array":
				src.owner = &storage.bytes
			default:
				src.owner = storage.bytes[:tt.length]
			}
			first := resource.DeepCopyStruct(src).(*object)
			second := resource.DeepCopyStruct(src).(*object)
			runtime.GC()
			for _, got := range []*object{first, second} {
				address := reflect.ValueOf(got.highPointer).Pointer()
				align := uintptr(reflect.TypeFor[high]().Align())
				if address%align != 0 {
					t.Fatalf("zero-sized pointer address %#x is not aligned to %d", address, align)
				}
				if address != reflect.ValueOf(got.lowPointer).Pointer() || address != reflect.ValueOf(byteOwnerPointer(got.owner)).Pointer() {
					t.Fatal("zero-sized types and byte owner lost their shared address")
				}
				if len(got.view) != 0 || cap(got.view) != 2 || got.highPointer != &got.view[:1][0] || got.index[&got.view[:1][0]] != 7 {
					t.Fatal("zero-sized slice lost its shape, pointer or map key")
				}
				if got.highPointer == src.highPointer {
					t.Fatal("zero-sized reference still uses source storage")
				}
				if view, ok := got.owner.([]byte); ok {
					if len(view) != tt.length || cap(view) != 3 {
						t.Fatal("byte owner slice shape changed")
					}
					for _, value := range view[len(view):cap(view)] {
						if value != 0 {
							t.Fatal("byte owner slice copied values beyond len")
						}
					}
				}
			}
			if first.highPointer == second.highPointer {
				t.Fatal("independent copies share byte owner storage")
			}
			before := *byteOwnerPointer(second.owner)
			*byteOwnerPointer(first.owner) = 42
			if storage.bytes[0] != 7 || *byteOwnerPointer(second.owner) != before {
				t.Fatal("byte mutation escaped its copy")
			}
			// Keep the destination allocation alive only through zero-sized
			// references, then verify its nonzero contents through a weak pointer.
			owner := weak.Make(byteOwnerPointer(first.owner))
			first.owner = nil
			runtime.GC()
			live := owner.Value()
			if live == nil || *live != 42 {
				t.Fatal("GC reclaimed storage reachable through zero-sized references")
			}
			runtime.KeepAlive(first)
		})
	}
}

func byteOwnerPointer(owner any) *byte {
	switch value := owner.(type) {
	case *byte:
		return value
	case *[3]byte:
		return &value[0]
	case []byte:
		return &value[:cap(value)][0]
	default:
		panic("unexpected test owner type")
	}
}

func TestDeepCopyStructZeroSizeInteriorAlignment(t *testing.T) {
	tests := []struct {
		name string
		elem reflect.Type
	}{
		{name: "bytes", elem: reflect.TypeFor[byte]()},
		{name: "arrays", elem: reflect.TypeFor[[4]byte]()},
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
			storage := &holder{prefix: 1, data: [12]byte{7, 8, 9}}
			// This pointer-free object's view deliberately starts at an address
			// with weaker alignment than the zero-sized field inside it.
			bytes := reflect.SliceAt(reflect.TypeFor[byte](), reflect.ValueOf(storage).UnsafePointer(), int(reflect.TypeFor[holder]().Size())).Interface().([]byte)[1:13]
			view := reflect.SliceAt(tt.elem, reflect.ValueOf(&bytes[0]).UnsafePointer(), len(bytes)/int(tt.elem.Size()))
			src := &object{pointer: &storage.zero, index: map[*zero]int{&storage.zero: 7}, view: view.Interface()}
			offset := reflect.ValueOf(src.pointer).Pointer() - reflect.ValueOf(&bytes[0]).Pointer()
			got := resource.DeepCopyStruct(src).(*object)
			runtime.GC()
			address := reflect.ValueOf(got.pointer).Pointer()
			if address%uintptr(reflect.TypeFor[zero]().Align()) != 0 {
				t.Fatal("interior zero-sized pointer lost alignment")
			}
			copiedView := reflect.ValueOf(got.view)
			if copiedView.Type() != view.Type() || copiedView.Len() != view.Len() || copiedView.Cap() != view.Cap() {
				t.Fatal("interior view type or shape changed")
			}
			copiedBytes := reflect.SliceAt(reflect.TypeFor[byte](), copiedView.UnsafePointer(), len(bytes)).Interface().([]byte)
			fromSlice := reflect.NewAt(reflect.TypeFor[zero](), reflect.ValueOf(&copiedBytes[offset]).UnsafePointer()).Interface().(*zero)
			if fromSlice != got.pointer || got.index[fromSlice] != 7 {
				t.Fatal("interior offset or pointer map key changed")
			}
			if got.pointer == src.pointer || copiedBytes[offset] != 7 {
				t.Fatal("interior storage retained the source or lost its values")
			}
			copiedBytes[offset] = 42
			if storage.data[0] != 7 {
				t.Fatal("byte mutation escaped its copy")
			}
			runtime.KeepAlive(storage)
		})
	}
}
