package resource_test

import (
	"bytes"
	"runtime"
	"sync"
	"testing"
	"unsafe"

	"github.com/reconcile-kit/api/resource"
)

type splitCopyBlock struct {
	Number  int
	Pointer *unsafeCopyPayload
	Next    *splitCopyBlock
	Data    []byte
}

type splitCopyOpaqueBlock struct {
	Number  int
	Pointer unsafe.Pointer
	Next    *splitCopyBlock
	Data    []byte
}

type splitCopyGraph struct {
	Views  []any
	Typed  *[4]splitCopyBlock
	Opaque *[4]splitCopyOpaqueBlock
	Fields [4]**unsafeCopyPayload
	Raw    [4]*unsafe.Pointer
	Index  map[*splitCopyBlock]*splitCopyOpaqueBlock
	Self   *splitCopyGraph
}

func exerciseSplitViews(t *testing.T, input []byte) {
	t.Helper()
	if len(input) > 256 {
		return
	}
	c := choices{data: input}
	backing := new([4]splitCopyBlock)
	opaque := (*[4]splitCopyOpaqueBlock)(unsafe.Pointer(backing))
	src := &splitCopyGraph{Typed: backing, Opaque: opaque, Index: make(map[*splitCopyBlock]*splitCopyOpaqueBlock)}
	src.Self = src
	var values [4][]byte
	for i := range backing {
		block := &backing[i]
		block.Number = i + 1
		if c.next()%3 != 0 {
			block.Pointer = &unsafeCopyPayload{byte(i + 1)}
		}
		block.Next = &backing[c.next()%len(backing)]
		capacity := 1 + c.next()%7
		length := c.next() % (capacity + 1)
		block.Data = make([]byte, capacity)
		for j := range block.Data {
			block.Data[j] = byte(i + j + 1)
		}
		block.Data = block.Data[:length]
		values[i] = bytes.Clone(block.Data)
		src.Fields[i], src.Raw[i] = &block.Pointer, &opaque[i].Pointer
		src.Index[block] = &opaque[i]
		src.Views = append(src.Views, block, &opaque[i])
	}
	for i := range src.Views {
		j := c.next() % len(src.Views)
		src.Views[i], src.Views[j] = src.Views[j], src.Views[i]
	}
	byValue := c.next()%2 == 0
	clone := func() *splitCopyGraph {
		if byValue {
			value := resource.DeepCopyStruct(*src).(splitCopyGraph)
			return &value
		}
		return resource.DeepCopyStruct(src).(*splitCopyGraph)
	}
	first, second := clone(), clone()
	for _, got := range []*splitCopyGraph{first, second} {
		if unsafe.Pointer(got.Typed) == unsafe.Pointer(got.Opaque) || got.Self == src || got.Self.Self != got.Self || (got.Self == got) == byValue {
			t.Fatal("views were not split or root semantics changed")
		}
		for i, original := range backing {
			typed, raw := &got.Typed[i], &got.Opaque[i]
			if typed.Number != i+1 || raw.Number != i+1 || got.Fields[i] != &typed.Pointer || got.Raw[i] != &raw.Pointer || got.Index[typed] != raw || raw.Pointer != opaque[i].Pointer {
				t.Fatal("split field ownership, map key, values or opaque identity changed")
			}
			if original.Pointer == nil {
				if typed.Pointer != nil {
					t.Fatal("nil pointer changed")
				}
			} else if typed.Pointer == original.Pointer || typed.Pointer[0] != byte(i+1) {
				t.Fatal("typed pointer was not independently copied")
			}
			for j := range backing {
				if original.Next == &backing[j] && (typed.Next != &got.Typed[j] || raw.Next != typed.Next) {
					t.Fatal("typed cycles crossed into an opaque owner")
				}
			}
			for _, data := range [][]byte{typed.Data, raw.Data} {
				if !bytes.Equal(data, values[i]) || cap(data) != cap(original.Data) {
					t.Fatal("slice value or shape changed")
				}
				for _, b := range data[len(data):cap(data)] {
					if b != 0 {
						t.Fatal("nonzero spare tail")
					}
				}
			}
			if len(typed.Data) > 0 && &typed.Data[0] != &raw.Data[0] {
				t.Fatal("identical slice references were duplicated across views")
			}
		}
		for _, view := range got.Views {
			switch view := view.(type) {
			case *splitCopyBlock:
				if view != &got.Typed[view.Number-1] {
					t.Fatal("typed view was duplicated")
				}
			case *splitCopyOpaqueBlock:
				if view != &got.Opaque[view.Number-1] {
					t.Fatal("opaque view was duplicated")
				}
			}
		}
	}
	var wg sync.WaitGroup
	for index, got := range []*splitCopyGraph{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range got.Typed {
				got.Typed[i].Number = index + 101
				if got.Typed[i].Pointer != nil {
					got.Typed[i].Pointer[0] = byte(index + 101)
				}
				if len(got.Typed[i].Data) > 0 {
					got.Typed[i].Data[0] = byte(index + 101)
				}
				*got.Raw[i] = nil
			}
		}()
	}
	wg.Wait()
	for i, original := range backing {
		if original.Number != i+1 || !bytes.Equal(original.Data, values[i]) || original.Pointer != nil && original.Pointer[0] != byte(i+1) {
			t.Fatal("source changed")
		}
		if first.Typed[i].Number != 101 || second.Typed[i].Number != 102 || first.Opaque[i].Number != i+1 || second.Opaque[i].Number != i+1 {
			t.Fatal("mutations crossed copies or conflicting views")
		}
		if original.Pointer != nil && (first.Typed[i].Pointer[0] != 101 || second.Typed[i].Pointer[0] != 102) {
			t.Fatal("pointer mutations crossed copies")
		}
	}
	runtime.KeepAlive(src)
}

func FuzzDeepCopySplitContract(f *testing.F) {
	f.Add([]byte{})
	for seed := range 32 {
		data := make([]byte, 128)
		for i := range data {
			data[i] = byte(seed*23 + i*13)
		}
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		exerciseSplitViews(t, data)
		exerciseUnsafeOverlap(t, data)
		exerciseStorageGraph(t, data)
		exerciseValueGraph(t, data)
		exerciseDetachedRoot(t, data)
	})
}
