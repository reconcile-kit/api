package resource_test

import (
	"bytes"
	"runtime"
	"sync"
	"testing"

	"github.com/reconcile-kit/api/resource"
)

type detachedRoot struct {
	Number        int
	Data          []byte
	Header        *[]byte
	HeaderAlias   *[]byte
	Value         any
	Slot          *any
	SlotAlias     *any
	NumberPointer *int
	NumberAlias   *int
	Index         map[*int]any
	MapHeader     *map[*int]any
}

func exerciseDetachedRoot(t *testing.T, input []byte) {
	t.Helper()
	c := choices{data: input}
	capacity := 1 + c.next()%16
	length := c.next() % (capacity + 1)
	backing := make([]byte, capacity)
	for i := range backing {
		backing[i] = byte(1 + c.next()%200)
	}
	src := &detachedRoot{Number: c.next(), Data: backing[:length], Index: make(map[*int]any)}
	src.Header = &src.Data
	src.HeaderAlias = src.Header
	src.Slot = &src.Value
	src.SlotAlias = src.Slot
	src.NumberPointer = &src.Number
	src.NumberAlias = src.NumberPointer
	src.MapHeader = &src.Index
	switch c.next() % 4 {
	case 0:
		src.Value = src.Slot
	case 1:
		src.Value = src.Header
	case 2:
		src.Value = src.Index
	case 3:
		src.Value = (*detachedRoot)(nil)
	}
	src.Index[src.NumberPointer] = *src
	first := resource.DeepCopyStruct(*src).(detachedRoot)
	second := resource.DeepCopyStruct(*src).(detachedRoot)
	for _, got := range []*detachedRoot{&first, &second} {
		if got.Header != got.HeaderAlias || got.Slot != got.SlotAlias || got.NumberPointer != got.NumberAlias || got.NumberPointer == &got.Number || got.Header == &got.Data || got.Slot == &got.Value || got.MapHeader == &got.Index {
			t.Fatal("field pointers were rebound to the returned value or lost aliases")
		}
		if *got.NumberPointer != src.Number || got.Number != src.Number || len(got.Data) != length || cap(got.Data) != capacity || !bytes.Equal(got.Data, src.Data) {
			t.Fatal("root or detached field values changed")
		}
		if &got.Data[:capacity][0] != &(*got.Header)[:capacity][0] {
			t.Fatal("pointer to slice header lost identical backing storage")
		}
		nested, ok := got.Index[got.NumberPointer].(detachedRoot)
		if !ok || nested.Header != got.Header || nested.Slot != got.Slot || nested.NumberPointer != got.NumberPointer || nested.MapHeader != got.MapHeader {
			t.Fatal("pointer map key or value cycle lost aliases")
		}
		if (*got.MapHeader)[got.NumberPointer].(detachedRoot).NumberPointer != got.NumberPointer {
			t.Fatal("pointer to map header lost map identity")
		}
		switch original := src.Value.(type) {
		case *any:
			if got.Value.(*any) != got.Slot || (*got.Slot).(*any) != got.Slot {
				t.Fatal("self-referential interface field changed")
			}
		case *[]byte:
			if got.Value.(*[]byte) != got.Header || (*got.Slot).(*[]byte) != got.Header {
				t.Fatal("interface header reference changed")
			}
		case map[*int]any:
			if got.Value.(map[*int]any)[got.NumberPointer].(detachedRoot).Header != got.Header {
				t.Fatal("interface map cycle changed")
			}
		case *detachedRoot:
			if original != nil || got.Value.(*detachedRoot) != nil {
				t.Fatal("typed nil changed")
			}
		}
	}
	originalAddresses := referencesOf(t, src, false)
	firstAddresses := referencesOf(t, first, true)
	for p := range firstAddresses {
		if originalAddresses[p] {
			t.Fatal("first detached graph retains original storage")
		}
	}
	for p := range referencesOf(t, second, true) {
		if originalAddresses[p] || firstAddresses[p] {
			t.Fatal("detached graphs share mutable storage")
		}
	}
	originalBytes := bytes.Clone(src.Data)
	originalNumber := src.Number
	mutate := func(got *detachedRoot, token byte) {
		for range 4 {
			*got.NumberPointer = int(token)
			if len(got.Data) > 0 {
				got.Data[0] = token
			}
			(*got.MapHeader)[got.NumberPointer] = token
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); mutate(&first, 201) }()
	go func() { defer wg.Done(); mutate(&second, 202) }()
	wg.Wait()
	if src.Number != originalNumber || !bytes.Equal(src.Data, originalBytes) {
		t.Fatal("detached copy mutation changed original")
	}
	if *first.NumberPointer != 201 || *second.NumberPointer != 202 || first.Number != originalNumber || second.Number != originalNumber || first.Index[first.NumberPointer] != byte(201) || second.Index[second.NumberPointer] != byte(202) {
		t.Fatal("detached copies lost value semantics or isolation")
	}
	*first.Header = nil
	*first.Slot = "changed"
	if len(first.Data) != length || len(second.Data) != length || len(*second.Header) != length || *second.Slot == "changed" {
		t.Fatal("header or interface mutation escaped detached field")
	}
	runtime.KeepAlive(src)
	runtime.KeepAlive(first)
	runtime.KeepAlive(second)
}
func FuzzDeepCopyDetachedFields(f *testing.F) {
	for i := range 32 {
		data := make([]byte, 24)
		for j := range data {
			data[j] = byte(i*13 + j*7)
		}
		f.Add(data)
	}
	f.Add([]byte{})
	f.Fuzz(exerciseDetachedRoot)
}
