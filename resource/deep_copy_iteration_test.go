package resource_test

import (
	"reflect"
	"runtime"
	"sync"
	"testing"
	"unsafe"
	"weak"

	"github.com/reconcile-kit/api/resource"
)

// Every view below is made from complete elements of one backing array. The
// oracle checks array aliases, not aliases between distinct slice views.
func TestDeepCopyStructArrayUnionBridge(t *testing.T) {
	t.Parallel()
	type item struct {
		Value  int
		Next   *item
		Header *[]byte
		Data   []byte
		Slot   *any
		Any    any
	}
	type graph struct {
		Roots []any
		Index map[*item]int
	}
	backing := make([]item, 12)
	for i := range backing {
		backing[i].Value = i + 1
		backing[i].Next = &backing[(i+1)%len(backing)]
		backing[i].Data = []byte{byte(i + 1)}
		backing[i].Header = &backing[(i+3)%len(backing)].Data
		backing[i].Slot = &backing[(i+5)%len(backing)].Any
		backing[i].Any = &backing[(i+7)%len(backing)]
	}
	src := &graph{Roots: []any{
		(*[3]item)(backing[6:]),
		(*[4]item)(backing[1:]),
		(*[4]item)(backing[4:]),
		(*[4]item)(backing[8:]),
		(*[2]item)(backing),
	}, Index: make(map[*item]int)}
	for i := range backing {
		src.Index[&backing[i]] = i + 1
	}
	first := resource.DeepCopyStruct(src).(*graph)
	second := resource.DeepCopyStruct(src).(*graph)
	for _, got := range []*graph{first, second} {
		base := got.Roots[4].(*[2]item)
		nodes := make([]*item, 12)
		node := &base[0]
		for i := range nodes {
			nodes[i] = node
			if node == &backing[i] || node.Value != i+1 || node.Data[0] != byte(i+1) || got.Index[node] != i+1 {
				t.Fatalf("node %d: contents, cycle or translated pointer key changed", i)
			}
			node = node.Next
		}
		if node != nodes[0] {
			t.Fatal("cycle did not close")
		}
		for i, node := range nodes {
			if node.Header != &nodes[(i+3)%12].Data || node.Slot != &nodes[(i+5)%12].Any || node.Any != nodes[(i+7)%12] {
				t.Fatalf("node %d: pointer to header/interface changed", i)
			}
		}
		starts := []int{6, 1, 4, 8, 0}
		for j, root := range got.Roots {
			a := reflect.ValueOf(root).Elem()
			for i := range a.Len() {
				if a.Index(i).Addr().Pointer() != reflect.ValueOf(nodes[starts[j]+i]).Pointer() {
					t.Fatalf("array %d element %d lost union storage", j, i)
				}
			}
		}
	}
	first.Roots[0].(*[3]item)[0].Data[0] = 99
	if backing[6].Data[0] != 7 || second.Roots[0].(*[3]item)[0].Data[0] != 7 {
		t.Fatal("mutation escaped copy")
	}
}

func TestDeepCopyStructPointerToHeadersFillOrder(t *testing.T) {
	t.Parallel()
	type object struct {
		Header   *[]byte
		Map      *map[*any]any
		Slot     *any
		Indirect **object
		Self     *object
		Data     []byte
		Values   map[*any]any
		Value    any
	}
	src := &object{Data: []byte{1, 2, 3}, Values: make(map[*any]any)}
	src.Header, src.Map, src.Slot, src.Indirect, src.Self = &src.Data, &src.Values, &src.Value, &src.Self, src
	src.Value = src.Slot
	src.Values[src.Slot] = src
	first := resource.DeepCopyStruct(src).(*object)
	second := resource.DeepCopyStruct(src).(*object)
	for _, got := range []*object{first, second} {
		if got.Header != &got.Data || got.Map != &got.Values || got.Slot != &got.Value || got.Indirect != &got.Self || got.Self != got || *got.Indirect != got || got.Value != got.Slot || got.Values[got.Slot] != got {
			t.Fatal("reentrant field population lost a header, cycle or pointer map key")
		}
	}
	var wg sync.WaitGroup
	for i, got := range []*object{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				(*got.Header)[0] = byte(i + 10)
				(*got.Map)[got.Slot] = i
				*got.Slot = i
			}
		}()
	}
	wg.Wait()
	if src.Data[0] != 1 || src.Value != src.Slot || src.Values[src.Slot] != src || first.Data[0] != 10 || second.Data[0] != 11 || first.Value != 0 || second.Value != 1 {
		t.Fatal("parallel header mutations escaped a copy")
	}
}

func TestDeepCopyStructOpaqueIdentities(t *testing.T) {
	t.Parallel()
	type object struct {
		Fn         func() int
		Channel    chan int
		Unsafe     unsafe.Pointer
		NilFn      func()
		NilChannel chan int
		NilUnsafe  unsafe.Pointer
	}
	value := 7
	src := &object{Fn: func() int { return value }, Channel: make(chan int, 1), Unsafe: unsafe.Pointer(&value)}
	got := resource.DeepCopyStruct(src).(*object)
	if got.Fn() != 7 || reflect.ValueOf(got.Fn).Pointer() != reflect.ValueOf(src.Fn).Pointer() || got.Channel != src.Channel || got.Unsafe != src.Unsafe || got.NilFn != nil || got.NilChannel != nil || got.NilUnsafe != nil {
		t.Fatal("opaque value identity or typed nil changed")
	}
	value = 9
	if got.Fn() != 9 {
		t.Fatal("closure environment changed")
	}
	got.Channel <- 3
	if <-src.Channel != 3 {
		t.Fatal("channel identity changed")
	}
}

type releaseCopyRoot struct {
	Data    []byte
	Self    *releaseCopyRoot
	Index   map[*releaseCopyRoot]*releaseCopyRoot
	Padding [256]byte
}

//go:noinline
func copiesForReleaseCheck() (*releaseCopyRoot, *releaseCopyRoot, weak.Pointer[releaseCopyRoot], weak.Pointer[byte]) {
	src := &releaseCopyRoot{Data: make([]byte, 1024)}
	src.Self = src
	src.Index = map[*releaseCopyRoot]*releaseCopyRoot{src: src}
	return resource.DeepCopyStruct(src).(*releaseCopyRoot), resource.DeepCopyStruct(src).(*releaseCopyRoot), weak.Make(src), weak.Make(&src.Data[0])
}

func TestDeepCopyStructDoesNotRetainSource(t *testing.T) {
	first, second, original, buffer := copiesForReleaseCheck()
	for range 5 {
		runtime.GC()
		runtime.Gosched()
	}
	if original.Value() != nil || buffer.Value() != nil {
		t.Fatal("copy or metadata retains original graph")
	}
	if first.Self != first || second.Self != second || first.Index[first] != first || second.Index[second] != second {
		t.Fatal("GC lost copied cycles")
	}
	runtime.KeepAlive(first)
	runtime.KeepAlive(second)
}

func TestDeepCopyStructCrossingSliceKeepsArrayOwner(t *testing.T) {
	t.Parallel()
	type object struct {
		Outer   *[3]int
		Inner   *[2]int
		View    []int
		Element *int
		Index   map[*int]int
	}
	backing := []int{1, 2, 3, 4, 5}
	src := &object{
		Outer: (*[3]int)(backing), Inner: (*[2]int)(backing[1:]),
		View: backing[1:4], Element: &backing[1], Index: map[*int]int{&backing[1]: 7},
	}
	first := resource.DeepCopyStruct(src).(*object)
	second := resource.DeepCopyStruct(src).(*object)
	for _, got := range []*object{first, second} {
		if &got.Outer[1] != &got.Inner[0] || got.Element != &got.Outer[1] || got.Index[&got.Outer[1]] != 7 {
			t.Fatal("crossing slice detached the nested array or its element pointer from the outer array")
		}
		if *got.Outer != [3]int{1, 2, 3} || *got.Inner != [2]int{2, 3} || !reflect.DeepEqual(got.View, []int{2, 3, 4}) || cap(got.View) != 4 || got.View[:4][3] != 0 {
			t.Fatal("array or slice values/shape changed")
		}
	}
	first.Inner[0] = 9
	if first.Outer[1] != 9 || backing[1] != 2 || second.Outer[1] != 2 {
		t.Fatal("array mutation lost aliasing or isolation")
	}
}

func TestDeepCopyStructConvertedPointerHeaders(t *testing.T) {
	t.Parallel()
	type left struct {
		data   []byte `copy:"left"`
		header *[]byte
		value  any
	}
	type right struct {
		data   []byte `copy:"right"`
		header *[]byte
		value  any
	}
	type pointer *left
	type graph struct {
		Left  pointer
		Right *right
		Index map[*right]any
		Value any
	}
	type rootPointer *graph
	original := &left{data: []byte{1, 2}}
	original.header = &original.data
	original.value = (*right)(original)
	src := rootPointer(&graph{Left: original, Right: (*right)(original), Index: map[*right]any{(*right)(original): pointer(original)}, Value: *original})
	got := resource.DeepCopyStruct(src).(rootPointer)
	if (*right)((*left)(got.Left)) != got.Right || got.Left.header != &got.Right.data || got.Left.value != got.Right || got.Index[got.Right].(pointer) != got.Left {
		t.Fatal("converted pointers lost aliases, private header or pointer map key")
	}
	boxed := got.Value.(left)
	if boxed.header != got.Left.header || boxed.value != got.Right || &boxed.data[0] != &got.Right.data[0] {
		t.Fatal("non-addressable value lost reference aliases")
	}
	*got.Left.header = []byte{9}
	if got.Right.data[0] != 9 || boxed.data[0] != 1 || original.data[0] != 1 {
		t.Fatal("converted header mutation lost Go value semantics or isolation")
	}
}
