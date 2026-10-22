package resource_test

import (
	"bytes"
	"runtime"
	"sync"
	"testing"
	"unsafe"

	"github.com/reconcile-kit/api/resource"
)

type unsafeFuzzBlock struct {
	Number  int
	Payload *unsafeCopyPayload
	Data    []byte
	Next    *unsafeFuzzBlock
	Value   any
}
type unsafeFuzzWindow struct {
	Block unsafeFuzzBlock
	Last  int
}
type unsafeFuzzGraph struct {
	Windows []*unsafeFuzzWindow
	Index   map[*int]int
	Self    *unsafeFuzzGraph
}

func exerciseUnsafeOverlap(t *testing.T, input []byte) {
	t.Helper()
	if len(input) > 256 {
		return
	}
	c := choices{data: input}
	count := 2 + c.next()%7
	backing := make([]unsafeFuzzBlock, count+1)
	src := &unsafeFuzzGraph{Index: make(map[*int]int)}
	src.Self = src
	for i := range backing {
		block := &backing[i]
		block.Number = c.next() + i + 1
		block.Payload = &unsafeCopyPayload{byte(i + 1)}
		capacity := 1 + c.next()%8
		length := c.next() % (capacity + 1)
		block.Data = make([]byte, capacity)
		for j := range block.Data {
			block.Data[j] = byte(i + j + 1)
		}
		block.Data = block.Data[:length]
		block.Next = &backing[(i+1)%len(backing)]
		block.Value = &backing[c.next()%len(backing)]
		src.Index[&block.Number] = i + 1
	}
	starts := make([]int, count)
	for i := range count {
		starts[i] = i
	}
	for i := range starts {
		j := c.next() % count
		starts[i], starts[j] = starts[j], starts[i]
	}
	for _, i := range starts {
		// All windows lie inside the typed block array; their final integer
		// aliases the next block's first integer without changing the GC layout.
		src.Windows = append(src.Windows, (*unsafeFuzzWindow)(unsafe.Pointer((*[2]unsafeFuzzBlock)(backing[i:]))))
	}
	byValue := c.next()%2 == 0
	clone := func() *unsafeFuzzGraph {
		if byValue {
			value := resource.DeepCopyStruct(*src).(unsafeFuzzGraph)
			return &value
		}
		return resource.DeepCopyStruct(src).(*unsafeFuzzGraph)
	}
	numbers := make([]int, len(backing))
	data := make([][]byte, len(backing))
	for i := range backing {
		numbers[i] = backing[i].Number
		data[i] = bytes.Clone(backing[i].Data)
	}
	first, second := clone(), clone()
	check := func(got *unsafeFuzzGraph) []*unsafeFuzzBlock {
		if got.Self == nil || got.Self == src || got.Self.Self != got.Self || byValue && got.Self == got || !byValue && got.Self != got {
			t.Fatal("root identity changed")
		}
		nodes := make([]*unsafeFuzzBlock, len(backing))
		for j, index := range starts {
			nodes[index] = &got.Windows[j].Block
		}
		nodes[count] = nodes[count-1].Next
		for i, node := range nodes {
			if node == nil || node == &backing[i] || node.Number != numbers[i] || node.Payload == backing[i].Payload || node.Payload[0] != byte(i+1) || !bytes.Equal(node.Data, data[i]) || cap(node.Data) != cap(backing[i].Data) {
				t.Fatalf("node %d: value or isolation changed", i)
			}
			if node.Next != nodes[(i+1)%len(nodes)] || got.Index[&node.Number] != i+1 {
				t.Fatal("cycle or pointer map key changed")
			}
			for j := range backing {
				if backing[i].Value == &backing[j] && node.Value != nodes[j] {
					t.Fatal("interface pointer changed")
				}
			}
			for _, b := range node.Data[len(node.Data):cap(node.Data)] {
				if b != 0 {
					t.Fatal("nonzero tail")
				}
			}
		}
		for j, i := range starts {
			if &got.Windows[j].Last != &nodes[i+1].Number {
				t.Fatal("unsafe windows lost overlap")
			}
		}
		return nodes
	}
	a, b := check(first), check(second)
	var wg sync.WaitGroup
	for index, nodes := range [][]*unsafeFuzzBlock{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 4 {
				for _, node := range nodes {
					node.Number = 101 + index
					node.Payload[0] = byte(101 + index)
					if len(node.Data) > 0 {
						node.Data[0] = byte(101 + index)
					}
				}
			}
		}()
	}
	wg.Wait()
	for i := range backing {
		if backing[i].Number != numbers[i] || backing[i].Payload[0] != byte(i+1) || !bytes.Equal(backing[i].Data, data[i]) {
			t.Fatal("source was changed")
		}
		if a[i].Number != 101 || b[i].Number != 102 || a[i].Payload[0] != 101 || b[i].Payload[0] != 102 {
			t.Fatal("parallel copies lost isolation")
		}
	}
	runtime.KeepAlive(src)
	runtime.KeepAlive(backing)
}

func FuzzDeepCopyUnsafeContract(f *testing.F) {
	f.Add([]byte{})
	for seed := range 32 {
		data := make([]byte, 128)
		for i := range data {
			data[i] = byte(seed*19 + i*11)
		}
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		exerciseUnsafeOverlap(t, data)
		exerciseStorageGraph(t, data)
		exerciseValueGraph(t, data)
		exerciseDetachedRoot(t, data)
	})
}
