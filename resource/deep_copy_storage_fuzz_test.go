package resource_test

import (
	"bytes"
	"reflect"
	"runtime"
	"sync"
	"testing"

	"github.com/reconcile-kit/api/resource"
)

type storageFuzzNode struct {
	Value  int
	Data   []byte
	Next   *storageFuzzNode
	Header *[]byte
	Slot   *any
	Any    any
	Map    *map[*storageFuzzNode]*storageFuzzNode
}

type storageFuzzRoot struct {
	Entry   *storageFuzzNode
	Windows []any
	Index   map[*storageFuzzNode]*storageFuzzNode
	View    []storageFuzzNode
	Inline  [2]int
	Scalar  *int
	Self    *storageFuzzRoot
}

func exerciseStorageGraph(t *testing.T, input []byte) {
	t.Helper()
	if len(input) > 256 {
		return
	}
	c := choices{data: input}
	const count = 8
	backing := make([]storageFuzzNode, count)
	src := &storageFuzzRoot{Entry: &backing[0], Index: make(map[*storageFuzzNode]*storageFuzzNode), Inline: [2]int{17, 23}}
	src.Self = src
	src.Scalar = &src.Inline[c.next()%2]
	for i := range backing {
		n := &backing[i]
		n.Value = 1 + c.next()
		capacity := 1 + c.next()%8
		length := c.next() % (capacity + 1)
		n.Data = make([]byte, capacity)
		for j := range n.Data {
			n.Data[j] = byte(1 + (i+j)%200)
		}
		n.Data = n.Data[:length]
		n.Next = &backing[(i+1)%count]
		n.Header = &backing[c.next()%count].Data
		n.Slot = &backing[c.next()%count].Any
		n.Map = &src.Index
		switch c.next() % 4 {
		case 0:
			n.Any = &backing[c.next()%count]
		case 1:
			n.Any = src
		case 2:
			n.Any = src.Index
		default:
			n.Any = (*storageFuzzNode)(nil)
		}
		src.Index[n] = n.Next
	}
	// Variable array lengths, offsets, nesting and traversal order change which
	// regions must merge. No unsafe reinterpretation is needed: SliceAt sees
	// complete storageFuzzNode elements, and array conversions stay in bounds.
	starts := make([]int, 0, 12)
	for range 1 + c.next()%12 {
		start := c.next() % count
		length := 1 + c.next()%(count-start)
		element := reflect.TypeFor[storageFuzzNode]()
		if length%2 == 0 && c.next()%2 == 0 {
			element = reflect.ArrayOf(2, element)
			length /= 2
		}
		view := reflect.SliceAt(element, reflect.ValueOf(&backing[start]).UnsafePointer(), length)
		array := view.Convert(reflect.PointerTo(reflect.ArrayOf(length, element)))
		src.Windows = append(src.Windows, array.Interface())
		starts = append(starts, start)
	}
	start := c.next() % count
	length := c.next() % (count - start + 1)
	capacity := length + c.next()%(count-start-length+1)
	src.View = backing[start : start+length : start+capacity]
	byValue := c.next()%2 == 0
	clone := func() *storageFuzzRoot {
		if byValue {
			v := resource.DeepCopyStruct(*src).(storageFuzzRoot)
			return &v
		}
		return resource.DeepCopyStruct(src).(*storageFuzzRoot)
	}
	var originalValues [count]int
	var originalData [count][]byte
	for i := range backing {
		originalValues[i] = backing[i].Value
		originalData[i] = bytes.Clone(backing[i].Data)
	}
	first, second := clone(), clone()
	check := func(got *storageFuzzRoot) []*storageFuzzNode {
		owner := got.Self
		if owner == nil || owner == src || owner.Self != owner || (!byValue && owner != got) || (byValue && owner == got) {
			t.Fatal("root form or self cycle changed")
		}
		if got.Inline != src.Inline || got.Scalar != &owner.Inline[0] && got.Scalar != &owner.Inline[1] || *got.Scalar != *src.Scalar {
			t.Fatal("inline value or interior pointer changed")
		}
		nodes := make([]*storageFuzzNode, count)
		node := got.Entry
		for i := range count {
			if node == nil || node == &backing[i] || node.Value != originalValues[i] || !bytes.Equal(node.Data, originalData[i]) || cap(node.Data) != cap(backing[i].Data) {
				t.Fatalf("node %d: visible values or isolation changed", i)
			}
			for _, v := range node.Data[len(node.Data):cap(node.Data)] {
				if v != 0 {
					t.Fatal("nonzero slice tail")
				}
			}
			if got.Index[node] != node.Next {
				t.Fatal("pointer map key lost graph identity")
			}
			nodes[i] = node
			node = node.Next
		}
		if node != nodes[0] || owner.Entry != nodes[0] || len(got.Index) != count {
			t.Fatal("cycle or map length changed")
		}
		for i, n := range nodes {
			original := &backing[i]
			if n.Map != &owner.Index {
				t.Fatal("map header lost owner")
			}
			for j := range backing {
				if original.Header == &backing[j].Data && n.Header != &nodes[j].Data {
					t.Fatal("slice header lost owner")
				}
				if original.Slot == &backing[j].Any && n.Slot != &nodes[j].Any {
					t.Fatal("interface header lost owner")
				}
			}
			switch v := original.Any.(type) {
			case *storageFuzzNode:
				if v == nil {
					if n.Any.(*storageFuzzNode) != nil {
						t.Fatal("typed nil changed")
					}
					break
				}
				for j := range backing {
					if v == &backing[j] && n.Any != nodes[j] {
						t.Fatal("interface node changed")
					}
				}
			case *storageFuzzRoot:
				if n.Any != owner {
					t.Fatal("interface root changed")
				}
			case map[*storageFuzzNode]*storageFuzzNode:
				if n.Any.(map[*storageFuzzNode]*storageFuzzNode)[nodes[0]] != nodes[1] {
					t.Fatal("interface map changed")
				}
			}
		}
		for j, window := range got.Windows {
			a := reflect.ValueOf(window).Elem()
			offset := starts[j]
			var visit func(reflect.Value)
			visit = func(v reflect.Value) {
				if v.Kind() == reflect.Array {
					for k := range v.Len() {
						visit(v.Index(k))
					}
					return
				}
				if v.Addr().Interface().(*storageFuzzNode) != nodes[offset] {
					t.Fatalf("overlapping array lost element identity: starts=%v types=%v view=%d:%d:%d byValue=%v window=%d offset=%d", starts, reflect.ValueOf(src.Windows), start, start+length, start+capacity, byValue, j, offset)
				}
				offset++
			}
			visit(a)
		}
		if len(got.View) != length || cap(got.View) != capacity {
			t.Fatal("view shape changed")
		}
		for i, n := range got.View {
			if n.Value != originalValues[start+i] || !bytes.Equal(n.Data, originalData[start+i]) || n.Next != nodes[(start+i+1)%count] {
				t.Fatal("detached view values or links changed")
			}
		}
		for _, n := range got.View[length:capacity] {
			if !reflect.ValueOf(n).IsZero() {
				t.Fatal("view tail populated")
			}
		}
		return nodes
	}
	firstNodes, secondNodes := check(first), check(second)
	for i := range backing {
		if backing[i].Value != originalValues[i] || !bytes.Equal(backing[i].Data, originalData[i]) || backing[i].Next != &backing[(i+1)%count] {
			t.Fatal("copying changed source")
		}
	}
	var wg sync.WaitGroup
	for copyIndex, nodes := range [][]*storageFuzzNode{firstNodes, secondNodes} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 4 {
				for _, n := range nodes {
					n.Value = 1000 + copyIndex
					if len(n.Data) > 0 {
						n.Data[0] = byte(101 + copyIndex)
					}
					(*n.Map)[n] = n
				}
			}
		}()
	}
	wg.Wait()
	for i := range backing {
		if backing[i].Value != originalValues[i] || !bytes.Equal(backing[i].Data, originalData[i]) || src.Index[&backing[i]] != backing[i].Next {
			t.Fatal("copy mutation changed source")
		}
		if firstNodes[i].Value != 1000 || secondNodes[i].Value != 1001 || first.Index[firstNodes[i]] != firstNodes[i] || second.Index[secondNodes[i]] != secondNodes[i] {
			t.Fatal("parallel copies share mutable state")
		}
		if len(firstNodes[i].Data) > 0 && (firstNodes[i].Data[0] != 101 || secondNodes[i].Data[0] != 102) {
			t.Fatal("parallel copies share byte storage")
		}
	}
	runtime.KeepAlive(src)
}

func FuzzDeepCopyContract(f *testing.F) {
	f.Add([]byte{})
	for seed := range 64 {
		data := make([]byte, 128)
		for i := range data {
			data[i] = byte(seed*17 + i*13)
		}
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		exerciseValueGraph(t, data)
		exerciseDetachedRoot(t, data)
		exerciseStorageGraph(t, data)
	})
}
