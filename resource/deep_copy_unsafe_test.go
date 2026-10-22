package resource_test

import (
	"math"
	"runtime"
	"testing"
	"unsafe"
	"weak"

	"github.com/reconcile-kit/api/resource"
)

func TestDeepCopyStructUnsafeOverlappingStructs(t *testing.T) {
	t.Parallel()
	type pair struct{ First, Last int }
	type graph struct {
		Left, Right *pair
		Shared      *int
		Index       map[*int]int
	}
	backing := []int{1, 2, 3}
	// Each conversion keeps the size, alignment, and pointer-free layout of
	// the source [2]int. Both windows remain inside the same backing array.
	left := (*pair)(unsafe.Pointer((*[2]int)(backing)))
	right := (*pair)(unsafe.Pointer((*[2]int)(backing[1:])))
	src := &graph{Left: left, Right: right, Shared: &left.Last, Index: map[*int]int{&left.Last: 7}}
	first := resource.DeepCopyStruct(src).(*graph)
	second := resource.DeepCopyStruct(src).(*graph)
	for _, got := range []*graph{first, second} {
		if &got.Left.Last != &got.Right.First || got.Shared != &got.Right.First || got.Index[&got.Right.First] != 7 {
			t.Fatal("overlapping struct windows lost their shared field or pointer map key")
		}
		if *got.Left != (pair{First: 1, Last: 2}) || *got.Right != (pair{First: 2, Last: 3}) {
			t.Fatal("struct window values changed")
		}
	}
	first.Right.First = 9
	if first.Left.Last != 9 || backing[1] != 2 || second.Left.Last != 2 {
		t.Fatal("struct mutation lost aliasing or isolation")
	}
	runtime.KeepAlive(backing)
}

type unsafeCopyPayload [128]byte

type unsafeCopyWindow struct {
	First   int
	Pointer *unsafeCopyPayload
	Last    int
}

//go:noinline
func storeUnsafeCopyPayload(window *unsafeCopyWindow) weak.Pointer[unsafeCopyPayload] {
	payload := &unsafeCopyPayload{99}
	window.Pointer = payload
	return weak.Make(payload)
}

func TestDeepCopyStructUnsafeOverlappingStructGC(t *testing.T) {
	t.Parallel()
	type backing struct {
		First  int
		P      *unsafeCopyPayload
		Middle int
		Q      *unsafeCopyPayload
		Last   int
	}
	type graph struct {
		Left, Right *unsafeCopyWindow
		Shared      *int
	}
	original := &backing{First: 1, P: &unsafeCopyPayload{2}, Middle: 3, Q: &unsafeCopyPayload{4}, Last: 5}
	// The two windows have the same alternating scalar/pointer layout as
	// their corresponding portions of backing; no GC pointer is hidden.
	left := (*unsafeCopyWindow)(unsafe.Pointer(original))
	right := (*unsafeCopyWindow)(unsafe.Add(unsafe.Pointer(original), unsafe.Offsetof(original.Middle)))
	src := &graph{Left: left, Right: right, Shared: &original.Middle}
	got := resource.DeepCopyStruct(src).(*graph)
	if &got.Left.Last != &got.Right.First || got.Shared != &got.Right.First {
		t.Fatal("mixed-layout struct windows lost the shared scalar")
	}
	if got.Left.First != 1 || got.Right.First != 3 || got.Right.Last != 5 || got.Left.Pointer == original.P || got.Right.Pointer == original.Q || got.Left.Pointer[0] != 2 || got.Right.Pointer[0] != 4 {
		t.Fatal("mixed-layout values or pointer isolation changed")
	}
	retained := storeUnsafeCopyPayload(got.Right)
	runtime.GC()
	if retained.Value() == nil || got.Right.Pointer[0] != 99 {
		t.Fatal("GC lost a pointer written to the union after copying")
	}
	*got.Shared = 8
	if got.Left.Last != 8 || got.Right.First != 8 || original.Middle != 3 {
		t.Fatal("mixed-layout mutation lost aliases or changed source")
	}
	runtime.KeepAlive(got)
	runtime.KeepAlive(original)
}

type unsafeCopyFields struct {
	Data    []byte
	Map     map[int]*unsafeCopyPayload
	Any     any
	Fn      func() byte
	Channel chan *unsafeCopyPayload
	String  string
	Pointer unsafe.Pointer
}
type unsafeCopyBlock struct {
	Number uintptr
	Fields unsafeCopyFields
}
type unsafeCopyFieldsWindow struct {
	Block unsafeCopyBlock
	Last  uintptr
}

//go:noinline
func populateUnsafeCopyFields(fields *unsafeCopyFields) []weak.Pointer[unsafeCopyPayload] {
	refs := make([]weak.Pointer[unsafeCopyPayload], 0, 6)
	next := func(value byte) *unsafeCopyPayload {
		p := &unsafeCopyPayload{value}
		refs = append(refs, weak.Make(p))
		return p
	}
	data := next(1)
	fields.Data = data[:]
	fields.Map = map[int]*unsafeCopyPayload{7: next(2)}
	fields.Any = next(3)
	captured := next(4)
	fields.Fn = func() byte { return captured[0] }
	fields.Channel = make(chan *unsafeCopyPayload, 1)
	fields.Channel <- next(5)
	fields.String = string([]byte("heap string for copied union"))
	fields.Pointer = unsafe.Pointer(next(6))
	return refs
}

func TestDeepCopyStructUnsafeUnionGCRepresentations(t *testing.T) {
	t.Parallel()
	type graph struct{ Left, Right *unsafeCopyFieldsWindow }
	backing := new([3]unsafeCopyBlock)
	src := &graph{
		Left:  (*unsafeCopyFieldsWindow)(unsafe.Pointer(backing)),
		Right: (*unsafeCopyFieldsWindow)(unsafe.Pointer((*[2]unsafeCopyBlock)(backing[1:]))),
	}
	// Each window ends in the next block's Number field, and both backing
	// blocks have precisely the fields and pointer layout of their views.
	got := resource.DeepCopyStruct(src).(*graph)
	if &got.Left.Last != &got.Right.Block.Number {
		t.Fatal("field-rich windows lost their overlap")
	}
	refs := populateUnsafeCopyFields(&got.Right.Block.Fields)
	runtime.GC()
	for i, ref := range refs {
		if ref.Value() == nil {
			t.Fatalf("GC lost representation %d in the union", i)
		}
	}
	fields := &got.Right.Block.Fields
	if fields.Data[0] != 1 || fields.Map[7][0] != 2 || fields.Any.(*unsafeCopyPayload)[0] != 3 || fields.Fn() != 4 || (<-fields.Channel)[0] != 5 || (*unsafeCopyPayload)(fields.Pointer)[0] != 6 || fields.String != "heap string for copied union" {
		t.Fatal("GC changed stored values")
	}
	runtime.KeepAlive(got)
	runtime.KeepAlive(backing)
}

//go:noinline
func storeUnsafeCopyIntegerBits(dst *uintptr) weak.Pointer[unsafeCopyPayload] {
	value := &unsafeCopyPayload{42}
	*dst = uintptr(unsafe.Pointer(value))
	return weak.Make(value)
}

func TestDeepCopyStructUnsafeUnionDoesNotScanIntegers(t *testing.T) {
	type graph struct{ Left, Right *unsafeCopyFieldsWindow }
	backing := new([3]unsafeCopyBlock)
	src := &graph{
		Left:  (*unsafeCopyFieldsWindow)(unsafe.Pointer(backing)),
		Right: (*unsafeCopyFieldsWindow)(unsafe.Pointer((*[2]unsafeCopyBlock)(backing[1:]))),
	}
	got := resource.DeepCopyStruct(src).(*graph)
	weakValue := storeUnsafeCopyIntegerBits(&got.Right.Last)
	for range 5 {
		runtime.GC()
		runtime.Gosched()
	}
	if weakValue.Value() != nil {
		t.Fatal("union GC metadata treats an integer as a pointer")
	}
	runtime.KeepAlive(got)
	runtime.KeepAlive(backing)
}

func TestDeepCopyStructUnsafeUnionInteriorAlignment(t *testing.T) {
	t.Parallel()
	type zero [0]uint64
	type holder struct {
		Prefix byte
		Zero   zero
		Data   [16]byte
	}
	type window struct{ Bytes [8]byte }
	type graph struct {
		Left, Right *window
		Zero        *zero
		Index       map[*zero]int
	}
	backing := &holder{Data: [16]byte{7, 8, 9}}
	base := unsafe.Pointer(backing)
	left := (*window)(unsafe.Add(base, 1))
	right := (*window)(unsafe.Add(base, 5))
	src := &graph{Left: left, Right: right, Zero: &backing.Zero, Index: map[*zero]int{&backing.Zero: 7}}
	got := resource.DeepCopyStruct(src).(*graph)
	offset := uintptr(unsafe.Pointer(src.Zero)) - uintptr(unsafe.Pointer(src.Left))
	if unsafe.Pointer(got.Zero) != unsafe.Add(unsafe.Pointer(got.Left), offset) || uintptr(unsafe.Pointer(got.Zero))%unsafe.Alignof(zero{}) != 0 {
		t.Fatal("union lost interior alignment or offset")
	}
	if &got.Left.Bytes[4] != &got.Right.Bytes[0] || got.Index[got.Zero] != 7 || got.Zero == src.Zero {
		t.Fatal("union lost overlap, pointer key or isolation")
	}
	got.Right.Bytes[0] = 99
	if got.Left.Bytes[4] != 99 || src.Right.Bytes[0] == 99 {
		t.Fatal("mutation lost overlap or changed source")
	}
	runtime.KeepAlive(backing)
}

func TestDeepCopyStructUnsafeOverlappingArrayTypes(t *testing.T) {
	t.Parallel()
	type graph struct {
		Bits   *[2]uint32
		Floats *[2]float32
		Shared *uint32
	}
	backing := []uint32{math.Float32bits(1.5), math.Float32bits(2.5), math.Float32bits(3.5)}
	src := &graph{
		Bits:   (*[2]uint32)(backing),
		Floats: (*[2]float32)(unsafe.Pointer((*[2]uint32)(backing[1:]))),
		Shared: &backing[1],
	}
	got := resource.DeepCopyStruct(src).(*graph)
	if unsafe.Pointer(&got.Bits[1]) != unsafe.Pointer(&got.Floats[0]) || got.Shared != &got.Bits[1] {
		t.Fatal("layout-compatible array types lost overlapping storage")
	}
	if *got.Bits != *src.Bits || *got.Floats != *src.Floats {
		t.Fatal("array representation bits changed")
	}
	got.Floats[0] = 9.5
	if got.Bits[1] != math.Float32bits(9.5) || backing[1] != math.Float32bits(2.5) {
		t.Fatal("array mutation lost aliasing or source isolation")
	}
	runtime.KeepAlive(backing)
}

type unsafeReleaseGraph struct{ Left, Right *unsafeCopyWindow }

//go:noinline
func makeUnsafeReleaseGraph() (*unsafeReleaseGraph, weak.Pointer[unsafeCopyPayload]) {
	type backing struct {
		A int
		P *unsafeCopyPayload
		B int
		Q *unsafeCopyPayload
		C int
	}
	original := &backing{P: &unsafeCopyPayload{1}, Q: &unsafeCopyPayload{2}}
	src := &unsafeReleaseGraph{
		Left:  (*unsafeCopyWindow)(unsafe.Pointer(original)),
		Right: (*unsafeCopyWindow)(unsafe.Add(unsafe.Pointer(original), unsafe.Offsetof(original.B))),
	}
	return resource.DeepCopyStruct(src).(*unsafeReleaseGraph), weak.Make(original.Q)
}

func TestDeepCopyStructUnsafeUnionReleasesSource(t *testing.T) {
	got, original := makeUnsafeReleaseGraph()
	for range 5 {
		runtime.GC()
		runtime.Gosched()
	}
	if original.Value() != nil {
		t.Fatal("union storage or its metadata retains source payloads")
	}
	if got.Left.Pointer[0] != 1 || got.Right.Pointer[0] != 2 || &got.Left.Last != &got.Right.First {
		t.Fatal("GC lost union contents or aliases")
	}
	runtime.KeepAlive(got)
}

func TestDeepCopyStructUnsafeOverlappingRootCycle(t *testing.T) {
	type window struct {
		Number int
		Next   *window
		Last   int
	}
	type block struct {
		Number int
		Next   *window
	}
	for _, byValue := range []bool{false, true} {
		name := "pointer"
		if byValue {
			name = "value"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			backing := &[3]block{{Number: 1}, {Number: 2}, {Number: 3}}
			left := (*window)(unsafe.Pointer(backing))
			right := (*window)(unsafe.Pointer((*[2]block)(backing[1:])))
			left.Next, right.Next = right, left
			clone := func() *window {
				if byValue {
					value := resource.DeepCopyStruct(*left).(window)
					return &value
				}
				return resource.DeepCopyStruct(left).(*window)
			}
			first, second := clone(), clone()
			for _, got := range []*window{first, second} {
				owner := got.Next.Next
				if owner == left || owner.Next != got.Next || got.Number != 1 || got.Last != 2 || got.Next.Number != 2 || got.Next.Last != 3 {
					t.Fatal("overlapping root cycle lost values or identity")
				}
				if &owner.Last != &got.Next.Number {
					t.Fatal("overlapping root and successor lost shared scalar")
				}
				if byValue && owner == got || !byValue && owner != got {
					t.Fatal("overlapping root lost value/pointer input semantics")
				}
			}
			first.Next.Number = 9
			if first.Next.Next.Last != 9 || second.Next.Number != 2 || left.Last != 2 {
				t.Fatal("mutation lost aliases or isolation")
			}
			if byValue && first.Last != 2 || !byValue && first.Last != 9 {
				t.Fatal("mutation incorrectly rebound the value root")
			}
			runtime.KeepAlive(backing)
		})
	}
}

func TestDeepCopyStructUnsafeArrayUnionBridge(t *testing.T) {
	t.Parallel()
	type block struct {
		Number  int
		Payload *unsafeCopyPayload
	}
	type window struct {
		Block block
		Last  int
	}
	type graph struct {
		Tail   *[2]block
		Bridge *window
		Middle *[2]block
		Head   *[2]block
		Index  map[*int]int
	}
	backing := make([]block, 5)
	for i := range backing {
		backing[i] = block{Number: i + 1, Payload: &unsafeCopyPayload{byte(i + 1)}}
	}
	// Head and Middle form an array union. Bridge overlaps its final block
	// and Tail's first scalar, joining otherwise adjacent array intervals.
	src := &graph{
		Tail: (*[2]block)(backing[3:]), Bridge: (*window)(unsafe.Pointer((*[2]block)(backing[2:]))),
		Middle: (*[2]block)(backing[1:]), Head: (*[2]block)(backing),
		Index: map[*int]int{&backing[3].Number: 7},
	}
	first := resource.DeepCopyStruct(src).(*graph)
	second := resource.DeepCopyStruct(src).(*graph)
	for _, got := range []*graph{first, second} {
		if &got.Head[1] != &got.Middle[0] || &got.Middle[1] != &got.Bridge.Block || &got.Bridge.Last != &got.Tail[0].Number || got.Index[&got.Bridge.Last] != 7 {
			t.Fatal("struct bridge lost array union storage or pointer map key")
		}
		nodes := []*block{&got.Head[0], &got.Head[1], &got.Bridge.Block, &got.Tail[0], &got.Tail[1]}
		for i, node := range nodes {
			if node.Number != i+1 || node.Payload == backing[i].Payload || node.Payload[0] != byte(i+1) {
				t.Fatal("array union bridge lost values or pointer isolation")
			}
		}
	}
	retained := storeUnsafeCopyPayload((*unsafeCopyWindow)(unsafe.Pointer(first.Tail)))
	runtime.GC()
	if retained.Value() == nil || first.Tail[0].Payload[0] != 99 {
		t.Fatal("GC lost a pointer stored in the extended array union")
	}
	first.Bridge.Last = 9
	if first.Tail[0].Number != 9 || second.Tail[0].Number != 4 || backing[3].Number != 4 || second.Tail[0].Payload[0] != 4 || backing[3].Payload[0] != 4 {
		t.Fatal("bridge mutation lost aliases or isolation")
	}
	runtime.KeepAlive(first)
	runtime.KeepAlive(backing)
}
