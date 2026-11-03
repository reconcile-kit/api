package resource

import (
	"reflect"
	"strconv"
	"unsafe"
)

// Compatible unsafe conversions can expose partially overlapping structs or
// arrays with different element types. Retain their complete typed regions
// until allocation so the destination has the union's actual GC pointer layout.
// These source references are invocation-local, never cached with type plans.
type copyPointerUnion struct {
	regions []copyReference
}

func (state *copyState) mergePointerStorage() bool {
	var previous *copyAllocation
	merged := false
	for i := range state.allocations {
		current := &state.allocations[i]
		ref := current.reference
		if ref.typ.Kind() != reflect.Pointer || current.size == 0 {
			continue
		}
		start := uintptr(ref.pointer)
		if previous == nil || start >= uintptr(previous.reference.pointer)+previous.size {
			previous = current
			continue
		}
		end := start + current.size
		base := uintptr(previous.reference.pointer)
		if end <= base+previous.size {
			continue
		}
		if previous.storage == nil || previous.storage.union == nil {
			initial := previous.reference
			if previous.storage != nil {
				initial = previous.storage.reference
			}
			previous.storage = &copyStorage{
				reference: copyReference{typ: reflect.TypeFor[[]byte](), pointer: previous.reference.pointer},
				alignment: previous.reference.typ.Elem(), pointerUnion: true,
				union: &copyPointerUnion{regions: []copyReference{initial}},
			}
		}
		region := ref
		if current.storage != nil {
			region = current.storage.reference
		}
		previous.storage.union.regions = append(previous.storage.union.regions, region)
		previous.size = end - base
		previous.storage.reference.length = int(previous.size)
		previous.storage.reference.capacity = int(previous.size)
		merged = true
	}
	return merged
}

func (storage *copyStorage) allocatePointerUnion() unsafe.Pointer {
	word := unsafe.Sizeof(uintptr(0))
	alignment := uintptr(storage.alignment.Align())
	prefix := uintptr(storage.reference.pointer) % alignment
	size := prefix + storage.reference.storageSize()
	var pointers []uintptr
	for _, region := range storage.union.regions {
		offset := prefix + uintptr(region.pointer) - uintptr(storage.reference.pointer)
		typ := region.typ.Elem()
		offsets := copyPointerOffsets(typ)
		count := 1
		if region.typ.Kind() == reflect.Slice {
			count = region.capacity
		}
		if len(offsets) == 0 {
			continue
		}
		for i := range count {
			for _, at := range offsets {
				pointers = append(pointers, offset+uintptr(i)*typ.Size()+at)
			}
		}
	}
	// Pointer-free unions use one repeated scalar word regardless of length.
	layout := []bool{false}
	if len(pointers) > 0 {
		layout = make([]bool, (size+word-1)/word)
		for _, offset := range pointers {
			layout[offset/word] = true
		}
		layout = layout[:copyLayoutPeriod(layout)]
	}
	return allocateCopyLayout(
		layout,
		storage.alignment,
		storage.reference.storageSize(),
		prefix,
	)
}

// allocateCopyLayout allocates a repetition of an exact GC layout. Padding
// outside the exposed payload stays zero, including any extra pointer slots.
func allocateCopyLayout(layout []bool, alignmentType reflect.Type, size, prefix uintptr) unsafe.Pointer {
	word := unsafe.Sizeof(uintptr(0))
	// A block must have no trailing alignment padding: its repetitions must
	// reproduce the source's pointer bitmap at exactly the same byte offsets.
	for uintptr(len(layout))*word%uintptr(alignmentType.Align()) != 0 {
		layout = append(layout, layout...)
	}
	fields := []reflect.StructField{{Name: "Alignment", Type: reflect.ArrayOf(0, alignmentType)}}
	for first := 0; first < len(layout); {
		end := first + 1
		for end < len(layout) && layout[end] == layout[first] {
			end++
		}
		typ := reflect.TypeFor[uintptr]()
		if layout[first] {
			typ = reflect.TypeFor[unsafe.Pointer]()
		}
		fields = append(fields, reflect.StructField{Name: "Words" + strconv.Itoa(first), Type: reflect.ArrayOf(end-first, typ)})
		first = end
	}
	block := reflect.StructOf(fields)
	blockSize := block.Size()
	// Keep the size and prefix separate to avoid overflowing their sum.
	count := size / blockSize
	tail := size % blockSize
	if tail != 0 {
		count++
	}
	if prefix != 0 && (tail == 0 || prefix > blockSize-tail) {
		count++
	}
	value := reflect.MakeSlice(reflect.SliceOf(block), int(count), int(count))
	return unsafe.Add(value.UnsafePointer(), prefix)
}

// This follows the public Go value representations also used by reflect's
// GC metadata builder: a string/slice has one pointer, an interface has two,
// and structs/arrays recursively contribute their fields/elements. A uintptr
// is deliberately absent: integer bits must never become GC pointers.
func copyPointerOffsets(typ reflect.Type) []uintptr {
	switch typ.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice, reflect.String, reflect.UnsafePointer:
		return []uintptr{0}
	case reflect.Interface:
		return []uintptr{0, unsafe.Sizeof(uintptr(0))}
	case reflect.Struct:
		var offsets []uintptr
		for i := range typ.NumField() {
			field := typ.Field(i)
			for _, offset := range copyPointerOffsets(field.Type) {
				offsets = append(offsets, field.Offset+offset)
			}
		}
		return offsets
	case reflect.Array:
		element := copyPointerOffsets(typ.Elem())
		if len(element) == 0 {
			return nil
		}
		var offsets []uintptr
		for i := range typ.Len() {
			for _, offset := range element {
				offsets = append(offsets, uintptr(i)*typ.Elem().Size()+offset)
			}
		}
		return offsets
	}
	return nil
}

// The shortest repeating prefix prevents a new reflect type for every union
// length when a chain repeats the same layout. The final block may be partial;
// its unused words remain zero and are never exposed through a copied view.
func copyLayoutPeriod(layout []bool) int {
	borders := make([]int, len(layout))
	for i := 1; i < len(layout); i++ {
		j := borders[i-1]
		for j > 0 && layout[i] != layout[j] {
			j = borders[j-1]
		}
		if layout[i] == layout[j] {
			j++
		}
		borders[i] = j
	}
	return len(layout) - borders[len(layout)-1]
}
