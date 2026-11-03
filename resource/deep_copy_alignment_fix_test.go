//go:build memorycheck

package resource

import (
	"fmt"
	"reflect"
	"runtime"
	"testing"
	"unsafe"
	"weak"
)

func TestDeepCopyAlignedSliceCapacities(t *testing.T) {
	t.Run("three_bytes", testCopyAlignedCapacities[[3]byte])
	t.Run("four_bytes", testCopyAlignedCapacities[[4]byte])
	t.Run("six_bytes", testCopyAlignedCapacities[[6]byte])
}

func testCopyAlignedCapacities[T [3]byte | [4]byte | [6]byte](t *testing.T) {
	t.Helper()
	type graph struct {
		Data []T
		Zero *[0]uint64
	}
	for _, capacity := range []int{17, 73, 257, 4097} {
		for residue := range 8 {
			t.Run(fmt.Sprintf("cap_%d_residue_%d", capacity, residue), func(t *testing.T) {
				t.Parallel()
				backing := new([4096]uint64)
				data := unsafe.Slice((*T)(unsafe.Add(unsafe.Pointer(backing), residue)), capacity)
				bytes := unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(data))), capacity*int(unsafe.Sizeof(data[0])))
				for i := range bytes {
					bytes[i] = byte(i%251 + 1)
				}
				length := capacity / 3
				src := &graph{Data: data[:length], Zero: (*[0]uint64)(unsafe.Pointer(&backing[1]))}
				first, second := DeepCopyStruct(src).(*graph), DeepCopyStruct(src).(*graph)
				for _, got := range []*graph{first, second} {
					if len(got.Data) != length || cap(got.Data) != capacity {
						t.Fatal("slice shape changed")
					}
					base := unsafe.Pointer(unsafe.SliceData(got.Data))
					if unsafe.Pointer(got.Zero) != unsafe.Add(base, 8-residue) || uintptr(unsafe.Pointer(got.Zero))%8 != 0 {
						t.Fatal("zero-sized interior alias lost its offset or alignment")
					}
					for i, value := range got.Data {
						if value != data[i] {
							t.Fatal("visible elements changed")
						}
					}
					var zero T
					for _, value := range got.Data[length:capacity] {
						if value != zero {
							t.Fatal("spare capacity was copied")
						}
					}
				}
				*(*byte)(unsafe.Pointer(unsafe.SliceData(first.Data))) = 0
				if bytes[0] != 1 || *(*byte)(unsafe.Pointer(unsafe.SliceData(second.Data))) != 1 {
					t.Fatal("copies share mutable storage")
				}
				runtime.KeepAlive(backing)
			})
		}
	}
}

type alignedFixPayload [128]byte

type alignedFixSlot struct {
	Pointer *alignedFixPayload
	Number  uintptr
}

//go:noinline
func storeAlignedFixPointer(slot *alignedFixSlot) weak.Pointer[alignedFixPayload] {
	value := &alignedFixPayload{71}
	slot.Pointer = value
	return weak.Make(value)
}

//go:noinline
func storeAlignedFixInteger(slot *alignedFixSlot) weak.Pointer[alignedFixPayload] {
	value := &alignedFixPayload{91}
	slot.Number = uintptr(unsafe.Pointer(value))
	return weak.Make(value)
}

//go:noinline
func exerciseAlignedFixGC(t *testing.T, count int, prefix uintptr) (weak.Pointer[alignedFixSlot], []weak.Pointer[alignedFixPayload]) {
	t.Helper()
	typ := reflect.TypeFor[alignedFixSlot]()
	layout := copyAlignedLayout(typ, uintptr(typ.Align()), prefix)
	pointer := allocateCopyLayout(
		layout,
		typ,
		uintptr(count)*typ.Size(),
		prefix,
	)
	// The shifted view spans block boundaries within a single allocation.
	data := unsafe.Slice((*alignedFixSlot)(pointer), count)
	live := make([]weak.Pointer[alignedFixPayload], count)
	dead := make([]weak.Pointer[alignedFixPayload], count)
	for i := range data {
		live[i] = storeAlignedFixPointer(&data[i])
		dead[i] = storeAlignedFixInteger(&data[i])
	}
	for range 5 {
		runtime.GC()
	}
	for i := range data {
		if live[i].Value() == nil || data[i].Pointer[0] != 71 {
			t.Fatal("GC missed a pointer written across block boundaries")
		}
		if dead[i].Value() != nil {
			t.Fatal("GC scanned integer bits as a pointer")
		}
	}
	probe := weak.Make(&data[0])
	runtime.KeepAlive(data)
	return probe, live
}

func TestDeepCopyAlignedSliceRotatedGC(t *testing.T) {
	for _, count := range []int{1, 2, 17} {
		for _, prefix := range []uintptr{0, unsafe.Sizeof(uintptr(0))} {
			t.Run(fmt.Sprintf("count_%d_prefix_%d", count, prefix), func(t *testing.T) {
				storage, payloads := exerciseAlignedFixGC(t, count, prefix)
				for range 5 {
					runtime.GC()
				}
				if storage.Value() != nil {
					t.Fatal("shifted storage retained after dropping its last pointer")
				}
				for _, payload := range payloads {
					if payload.Value() != nil {
						t.Fatal("shifted storage retained a payload")
					}
				}
			})
		}
	}
}
