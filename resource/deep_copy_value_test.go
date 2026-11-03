package resource_test

import (
	"encoding/json"
	"reflect"
	"runtime"
	"sync"
	"testing"

	"github.com/reconcile-kit/api/resource"
)

func TestDeepCopyStructValueBuffers(t *testing.T) {
	for _, tt := range copyBufferCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := *newCopyResource()
			first := resource.DeepCopyStruct(src).(copyResource)
			second := resource.DeepCopyStruct(src).(copyResource)
			if !reflect.DeepEqual(src, first) || !reflect.DeepEqual(src, second) {
				t.Fatal("struct value copy lost fields")
			}
			tt.buffer(&first)[1] = '2'
			tt.buffer(&second)[1] = '3'
			first.Labels["team"], second.Labels["team"] = "first", "second"
			if string(tt.buffer(&src)) != "[1]" || string(tt.buffer(&first)) != "[2]" || string(tt.buffer(&second)) != "[3]" {
				t.Fatal("struct values share mutable buffers across copies")
			}
			if src.Labels["team"] != "original" || first.Labels["team"] != "first" || second.Labels["team"] != "second" {
				t.Fatal("struct values share maps across copies")
			}
		})
	}
}

func TestDeepCopyStructValueRootReferences(t *testing.T) {
	t.Parallel()
	type object struct {
		array   [2]int
		self    *object
		elem    *int
		index   map[*int]int
		data    json.RawMessage
		typed   any
		missing []byte
		empty   []byte
	}
	src := &object{array: [2]int{1, 2}, data: json.RawMessage(`[1]`), typed: (*int)(nil), empty: []byte{}}
	src.self = src
	src.elem = &src.array[1]
	src.index = map[*int]int{src.elem: 7}
	first := resource.DeepCopyStruct(*src).(object)
	second := resource.DeepCopyStruct(*src).(object)
	runtime.GC()
	for _, got := range []*object{&first, &second} {
		// Passing *src as a value has already copied its inline array, while
		// self and elem still refer to src. Preserve that graph in the copy.
		if got.self == src || got.self == got || got.self.self != got.self || got.elem != &got.self.array[1] || got.self.elem != got.elem {
			t.Fatal("value root lost its separately copied owner, interior pointer or cycle")
		}
		if got.index[got.elem] != 7 || got.self.index[got.elem] != 7 || got.array != [2]int{1, 2} {
			t.Fatal("value root lost inline values or pointer map keys")
		}
		if &got.data[0] != &got.self.data[0] || &got.data[0] == &src.data[0] {
			t.Fatal("value root lost repeated references or retained source storage")
		}
		if got.typed == nil || got.typed.(*int) != nil || got.missing != nil || got.empty == nil || len(got.empty) != 0 {
			t.Fatal("value root changed typed nil, nil or empty fields")
		}
	}
	*first.elem = 9
	first.data[1] = '2'
	first.index[first.elem] = 8
	if first.array[1] != 2 || first.self.array[1] != 9 || first.self.index[first.elem] != 8 || string(first.self.data) != "[2]" {
		t.Fatal("value root mutation did not follow the copied reference graph")
	}
	if src.array[1] != 2 || *second.elem != 2 || string(src.data) != "[1]" || string(second.data) != "[1]" || src.index[src.elem] != 7 || second.index[second.elem] != 7 {
		t.Fatal("value root mutation escaped its copy")
	}
}

func TestDeepCopyStructSinglePointerValue(t *testing.T) {
	t.Parallel()
	// A struct containing one pointer may be stored directly in an interface.
	type object struct{ node *privateCopyNode }
	node := &privateCopyNode{data: json.RawMessage(`[1]`)}
	node.next = node
	src := object{node: node}
	got := resource.DeepCopyStruct(src).(object)
	if got.node == src.node || got.node.next != got.node || string(got.node.data) != "[1]" {
		t.Fatal("single-pointer struct value lost isolation, contents or cycle")
	}
	got.node.data[1] = '2'
	if string(src.node.data) != "[1]" {
		t.Fatal("single-pointer struct value shares source storage")
	}
}

func TestDeepCopyStructValueConcurrentCopies(t *testing.T) {
	t.Parallel()
	src := *newCopyResource()
	const workers = 8
	copies := make([]copyResource, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := resource.DeepCopyStruct(src).(copyResource)
			for range 20 {
				got.Spec[1] = byte('2' + i)
				got.Labels["team"] = string(rune('a' + i))
			}
			copies[i] = got
		}()
	}
	wg.Wait()
	if string(src.Spec) != "[1]" || src.Labels["team"] != "original" {
		t.Fatal("concurrent value copies changed the source")
	}
	for i, got := range copies {
		if got.Spec[1] != byte('2'+i) || got.Labels["team"] != string(rune('a'+i)) {
			t.Fatal("concurrent value copies share mutable data")
		}
	}
}
