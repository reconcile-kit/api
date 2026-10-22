package resource_test

import (
	"encoding/binary"
	"math/rand/v2"
	"reflect"
	"runtime"
	"sync"
	"testing"

	"github.com/reconcile-kit/api/resource"
)

type fuzzCell struct {
	Value  int
	Data   []byte
	Next   *fuzzCell
	View   []fuzzCell
	Any    any
	Slot   *any
	Header *[]fuzzCell
	Bytes  *[]byte
	Scalar *int
	Owner  *fuzzRoot
}
type fuzzRoot struct {
	Entry *fuzzCell
	Index map[*fuzzCell]*fuzzCell
	Dict  map[string]any
	Cells [4]fuzzCell
	Self  *fuzzRoot
	View  []fuzzCell
}
type choices struct {
	data []byte
	at   int
}

func (c *choices) next() int {
	if c.at >= len(c.data) {
		return 0
	}
	v := int(c.data[c.at])
	c.at++
	return v
}
func (c *choices) view(data []fuzzCell) []fuzzCell {
	start := c.next() % 5
	end := start + c.next()%(5-start)
	capacity := end + c.next()%(5-end)
	return data[start:end:capacity]
}
func makeFuzzRoot(input []byte) *fuzzRoot {
	c := choices{data: input}
	src := &fuzzRoot{Index: make(map[*fuzzCell]*fuzzCell), Dict: make(map[string]any)}
	src.Self = src
	src.Entry = &src.Cells[c.next()%4]
	src.View = c.view(src.Cells[:])
	pools := [][]byte{{1, 2, 3, 4, 5, 6}, {7, 8, 9, 10, 11, 12}, {13, 14, 15, 16, 17, 18}}
	for i := range src.Cells {
		cell := &src.Cells[i]
		cell.Value = c.next() + i + 1
		pool := pools[c.next()%3]
		length := c.next() % 7
		capacity := length + c.next()%(7-length)
		cell.Data = pool[:length:capacity]
		if c.next()%7 == 0 {
			cell.Data = nil
		}
		cell.Next = &src.Cells[c.next()%4]
		cell.View = c.view(src.Cells[:])
		cell.Slot = &src.Cells[c.next()%4].Any
		cell.Header = &src.Cells[c.next()%4].View
		cell.Bytes = &src.Cells[c.next()%4].Data
		cell.Scalar = &src.Cells[c.next()%4].Value
		cell.Owner = src
		switch c.next() % 7 {
		case 0:
			cell.Any = src
		case 1:
			cell.Any = src.Dict
		case 2:
			cell.Any = cell.View
		case 3:
			cell.Any = &src.Cells[c.next()%4]
		case 4:
			cell.Any = (*fuzzRoot)(nil)
		case 5:
			cell.Any = src.Index
		default:
			cell.Any = nil
		}
		src.Index[cell] = cell.Next
	}
	src.Dict["self"] = src
	src.Dict["value"] = *src
	src.Dict["index"] = src.Index
	return src
}

type reference struct {
	address          uintptr
	typ              reflect.Type
	length, capacity int
}

func referencesOf(t *testing.T, root any, zeroTail bool) map[uintptr]bool {
	t.Helper()
	addresses := make(map[uintptr]bool)
	visited := make(map[reference]bool)
	var walk func(reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map:
			if v.IsNil() {
				return
			}
			ref := reference{address: uintptr(v.UnsafePointer()), typ: v.Type()}
			if v.Kind() == reflect.Slice {
				ref.length, ref.capacity = v.Len(), v.Cap()
			}
			if visited[ref] {
				return
			}
			visited[ref] = true
			addresses[ref.address] = true
			if zeroTail && v.Kind() == reflect.Slice {
				tail := v.Slice(v.Len(), v.Cap())
				for i := range tail.Len() {
					if !tail.Index(i).IsZero() {
						t.Fatalf("nonzero tail: %v len=%d cap=%d index=%d", v.Type(), v.Len(), v.Cap(), i)
					}
				}
			}
		}
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Slice, reflect.Array:
			for i := range v.Len() {
				walk(v.Index(i))
			}
		case reflect.Struct:
			for i := range v.NumField() {
				walk(v.Field(i))
			}
		case reflect.Map:
			it := v.MapRange()
			for it.Next() {
				walk(it.Key())
				walk(it.Value())
			}
		}
	}
	walk(reflect.ValueOf(root))
	return addresses
}
func checkFuzzRoot(t *testing.T, src *fuzzRoot, got *fuzzRoot) {
	t.Helper()
	owner := got.Self
	if owner == nil || owner == src || owner == got || owner.Self != owner {
		t.Fatal("value-root self cycle or visible values changed")
	}
	compareVisibleGraph(t, src, got)
	if len(got.View) != len(src.View) || cap(got.View) != cap(src.View) {
		t.Fatal("root view shape changed")
	}
	for i := range src.Cells {
		original := &src.Cells[i]
		cell := &got.Cells[i]
		if cell.Owner != owner || got.Index[&owner.Cells[i]] != cell.Next {
			t.Fatal("owner or pointer map identity lost")
		}
		if len(cell.Data) != len(original.Data) || cap(cell.Data) != cap(original.Data) || len(cell.View) != len(original.View) || cap(cell.View) != cap(original.View) {
			t.Fatal("cell slice shape changed")
		}
		for j := range src.Cells {
			if original.Next == &src.Cells[j] && cell.Next != &owner.Cells[j] {
				t.Fatal("Next lost copied owner")
			}
			if original.Slot == &src.Cells[j].Any && cell.Slot != &owner.Cells[j].Any {
				t.Fatal("interface-field pointer lost copied owner")
			}
			if original.Header == &src.Cells[j].View && cell.Header != &owner.Cells[j].View {
				t.Fatal("slice-header pointer lost copied owner")
			}
			if original.Bytes == &src.Cells[j].Data && cell.Bytes != &owner.Cells[j].Data {
				t.Fatal("byte-header pointer lost copied owner")
			}
			if original.Scalar == &src.Cells[j].Value && cell.Scalar != &owner.Cells[j].Value {
				t.Fatal("scalar-field pointer lost copied owner")
			}
		}
	}
	if got.Dict["self"].(*fuzzRoot) != owner || got.Dict["value"].(fuzzRoot).Self != owner {
		t.Fatal("map root cycles lost identity")
	}
}

// Compare values while translating pointer map keys through the copied owner.
func compareVisibleGraph(t *testing.T, original, cloned *fuzzRoot) {
	t.Helper()
	type pair struct{ source, destination reference }
	seen := make(map[pair]bool)
	var compare func(reflect.Value, reflect.Value)
	compare = func(src, dst reflect.Value) {
		if !src.IsValid() || !dst.IsValid() {
			if src.IsValid() != dst.IsValid() {
				t.Fatal("missing map entry")
			}
			return
		}
		if src.Type() != dst.Type() {
			t.Fatal("dynamic type changed")
		}
		switch src.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice:
			if src.IsNil() != dst.IsNil() {
				t.Fatal("nil changed")
			}
			if src.IsNil() {
				return
			}
			key := pair{source: reference{address: uintptr(src.UnsafePointer()), typ: src.Type()}, destination: reference{address: uintptr(dst.UnsafePointer()), typ: dst.Type()}}
			if src.Kind() == reflect.Slice {
				if src.Len() != dst.Len() || src.Cap() != dst.Cap() {
					t.Fatal("slice shape changed")
				}
				key.source.length, key.source.capacity = src.Len(), src.Cap()
				key.destination.length, key.destination.capacity = dst.Len(), dst.Cap()
			}
			if seen[key] {
				return
			}
			seen[key] = true
		}
		switch src.Kind() {
		case reflect.Pointer:
			compare(src.Elem(), dst.Elem())
		case reflect.Interface:
			if src.IsNil() != dst.IsNil() {
				t.Fatal("nil interface changed")
			}
			if !src.IsNil() {
				compare(src.Elem(), dst.Elem())
			}
		case reflect.Struct:
			for i := range src.NumField() {
				compare(src.Field(i), dst.Field(i))
			}
		case reflect.Slice, reflect.Array:
			for i := range src.Len() {
				compare(src.Index(i), dst.Index(i))
			}
		case reflect.Map:
			if src.Len() != dst.Len() {
				t.Fatal("map length changed")
			}
			it := src.MapRange()
			for it.Next() {
				key := it.Key()
				if key.Type() == reflect.TypeFor[*fuzzCell]() {
					found := false
					for i := range original.Cells {
						if key.Interface().(*fuzzCell) == &original.Cells[i] {
							key = reflect.ValueOf(&cloned.Self.Cells[i])
							found = true
							break
						}
					}
					if !found {
						t.Fatal("unexpected fixture pointer key")
					}
				}
				compare(it.Value(), dst.MapIndex(key))
			}
		default:
			if !reflect.DeepEqual(src.Interface(), dst.Interface()) {
				t.Fatal("scalar value changed")
			}
		}
	}
	compare(reflect.ValueOf(*original), reflect.ValueOf(*cloned))
}

func exerciseValueGraph(t *testing.T, input []byte) {
	t.Helper()
	if len(input) > 256 {
		return
	}
	src := makeFuzzRoot(input)
	first := resource.DeepCopyStruct(*src).(fuzzRoot)
	second := resource.DeepCopyStruct(*src).(fuzzRoot)
	checkFuzzRoot(t, src, &first)
	checkFuzzRoot(t, src, &second)
	original := referencesOf(t, src, false)
	copied := referencesOf(t, first, true)
	for address := range copied {
		if original[address] {
			t.Fatal("first copy retains a source address")
		}
	}
	for address := range referencesOf(t, second, true) {
		if original[address] || copied[address] {
			t.Fatal("second copy shares storage with source or first copy")
		}
	}
	var values [4]int
	var buffers [4][]byte
	for i := range src.Cells {
		values[i] = src.Cells[i].Value
		buffers[i] = append([]byte(nil), src.Cells[i].Data...)
	}
	mutate := func(got *fuzzRoot, token byte) {
		for range 4 {
			for i := range got.Self.Cells {
				got.Self.Cells[i].Value = int(token) + i
				if len(got.Self.Cells[i].Data) > 0 {
					got.Self.Cells[i].Data[0] = token
				}
			}
			got.Dict["mutation"] = token
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); mutate(&first, 101) }()
	go func() { defer wg.Done(); mutate(&second, 202) }()
	wg.Wait()
	for i := range src.Cells {
		if src.Cells[i].Value != values[i] || !reflect.DeepEqual(append([]byte(nil), src.Cells[i].Data...), buffers[i]) {
			t.Fatal("copy mutation escaped into source")
		}
		if first.Self.Cells[i].Value != 101+i || second.Self.Cells[i].Value != 202+i || first.Cells[i].Value != values[i] || second.Cells[i].Value != values[i] {
			t.Fatal("copies lost scalar isolation or value-root semantics")
		}
	}
	if src.Dict["mutation"] != nil || first.Dict["mutation"] != byte(101) || second.Dict["mutation"] != byte(202) {
		t.Fatal("copies share maps")
	}
	runtime.KeepAlive(src)
	runtime.KeepAlive(first)
	runtime.KeepAlive(second)
}
func FuzzDeepCopyValueGraphs(f *testing.F) {
	f.Add([]byte{})
	for seed := range uint64(64) {
		data := make([]byte, 96)
		rng := rand.New(rand.NewPCG(seed, 17))
		for i := 0; i < len(data); i += 8 {
			binary.LittleEndian.PutUint64(data[i:], rng.Uint64())
		}
		f.Add(data)
	}
	f.Fuzz(exerciseValueGraph)
}
