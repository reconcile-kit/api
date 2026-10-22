package resource

import (
	"reflect"
	"unsafe"
)

// Aligned slices repeat a layout derived solely from their element type,
// required alignment and starting residue. Capacity only changes the number
// of blocks, so new capacities cannot introduce permanent reflect types.
func (storage *copyStorage) allocateAlignedSlice() unsafe.Pointer {
	ref := storage.reference
	prefix := uintptr(ref.pointer) % uintptr(storage.alignment.Align())
	layout := copyAlignedLayout(ref.typ.Elem(), uintptr(storage.alignment.Align()), prefix)
	return allocateCopyLayout(
		layout,
		storage.alignment,
		ref.storageSize(),
		prefix,
	)
}

// A typed element can cross block boundaries. Rotate its pointer bitmap by
// prefix instead of requiring that prefix be a whole number of elements.
// This also handles byte-aligned arrays whose sizes share a factor with the
// required alignment, such as [4]byte starting at offset 1 modulo 8.
func copyAlignedLayout(elem reflect.Type, alignment, prefix uintptr) []bool {
	offsets := copyPointerOffsets(elem)
	if len(offsets) == 0 {
		return []bool{false}
	}

	word := unsafe.Sizeof(uintptr(0))
	blockSize := elem.Size()
	for blockSize%alignment != 0 {
		blockSize *= 2
	}
	layout := make([]bool, blockSize/word)
	for start := uintptr(0); start < blockSize; start += elem.Size() {
		for _, offset := range offsets {
			layout[(prefix+start+offset)%blockSize/word] = true
		}
	}
	return layout
}
