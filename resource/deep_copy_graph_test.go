package resource_test

import (
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/reconcile-kit/api/conditions"
	"github.com/reconcile-kit/api/resource"
)

type privateCopyNode struct {
	value int
	data  json.RawMessage
	next  *privateCopyNode
}

func TestDeepCopyStructPrivateFields(t *testing.T) {
	t.Parallel()
	type secret struct {
		count int
		data  json.RawMessage
		node  *privateCopyNode
	}
	type object struct {
		field secret
		items map[string]secret
		value any
		array [1]secret
	}
	original := secret{count: 7, data: json.RawMessage(`[1]`), node: &privateCopyNode{value: 1}}
	src := &object{
		field: original,
		items: map[string]secret{"key": original},
		value: original,
		array: [1]secret{original},
	}
	first := resource.DeepCopyStruct(src).(*object)
	second := resource.DeepCopyStruct(src).(*object)

	for _, value := range []secret{first.field, first.items["key"], first.value.(secret), first.array[0]} {
		if value.count != 7 || string(value.data) != "[1]" || value.node.value != 1 {
			t.Fatalf("private fields lost: %+v", value)
		}
	}
	first.field.data[1] = '2'
	first.items["key"].node.value = 2
	if first.value.(secret).node != first.field.node {
		t.Fatal("a repeated private pointer was copied more than once")
	}
	for _, obj := range []*object{src, second} {
		for _, value := range []secret{obj.field, obj.items["key"], obj.value.(secret), obj.array[0]} {
			if string(value.data) != "[1]" || value.node.value != 1 {
				t.Fatal("private fields share mutable data across copies")
			}
		}
	}
}

func TestDeepCopyStructPointerCycles(t *testing.T) {
	t.Parallel()
	src := &privateCopyNode{value: 1, data: json.RawMessage(`[1]`)}
	src.next = &privateCopyNode{value: 2, next: src}
	first := resource.DeepCopyStruct(src).(*privateCopyNode)
	second := resource.DeepCopyStruct(src).(*privateCopyNode)
	for _, got := range []*privateCopyNode{first, second} {
		if got.next.next != got || got.next == src.next {
			t.Fatal("pointer cycle was not rebuilt inside the copy")
		}
	}
	first.next.value = 9
	first.data[1] = '9'
	if src.next.value != 2 || second.next.value != 2 || string(src.data) != "[1]" {
		t.Fatal("pointer cycles share mutable data across copies")
	}
}

func TestDeepCopyStructContainerCycles(t *testing.T) {
	t.Parallel()
	type dictionary map[string]any
	type sequence []any
	type object struct {
		dictionary dictionary
		alias      map[string]any
		sequence   sequence
		sliceAlias []any
	}
	src := &object{dictionary: make(dictionary), sequence: make(sequence, 2, 5)}
	src.alias = src.dictionary
	src.sliceAlias = src.sequence
	src.dictionary["self"] = src.dictionary
	src.dictionary["root"] = src
	src.dictionary["slice"] = src.sequence
	src.sequence[0] = src.sequence
	src.sequence[1] = src.dictionary
	first := resource.DeepCopyStruct(src).(*object)
	second := resource.DeepCopyStruct(src).(*object)

	if first.dictionary["root"].(*object) != first {
		t.Fatal("map points to the wrong root")
	}
	first.dictionary["self"].(dictionary)["added"] = true
	if first.alias["added"] != true || first.sequence[1].(dictionary)["added"] != true {
		t.Fatal("map aliases were lost")
	}
	first.sequence[0].(sequence)[1] = "changed"
	if first.sliceAlias[1] != "changed" || first.dictionary["slice"].(sequence)[1] != "changed" {
		t.Fatal("slice aliases were lost")
	}
	for _, obj := range []*object{src, second} {
		if _, exists := obj.dictionary["added"]; exists {
			t.Fatal("map mutation escaped its copy")
		}
		if _, ok := obj.sequence[1].(dictionary); !ok {
			t.Fatal("slice mutation escaped its copy")
		}
	}
}

func TestDeepCopyStructPointersIntoObjects(t *testing.T) {
	t.Parallel()
	type owner struct {
		value int
		node  privateCopyNode
	}
	type object struct {
		// Visit the interior pointers before the objects owning their storage.
		valuePointer *int
		nodePointer  *privateCopyNode
		bytePointer  *byte
		holder       *owner
		buffer       []byte
	}
	src := &object{holder: &owner{value: 1}, buffer: []byte{1, 2}}
	src.valuePointer = &src.holder.value
	src.nodePointer = &src.holder.node
	src.nodePointer.next = src.nodePointer
	src.bytePointer = &src.buffer[1]
	got := resource.DeepCopyStruct(src).(*object)
	if got.valuePointer != &got.holder.value || got.nodePointer != &got.holder.node {
		t.Fatal("interior pointer does not refer to its copied owner")
	}
	if got.nodePointer.next != got.nodePointer || got.bytePointer != &got.buffer[1] {
		t.Fatal("interior cycle or slice element pointer was lost")
	}
	*got.valuePointer = 9
	*got.bytePointer = 9
	if src.holder.value != 1 || src.buffer[1] != 2 {
		t.Fatal("interior pointer still refers to the source")
	}
}

func TestDeepCopyStructPointerMapKeys(t *testing.T) {
	t.Parallel()
	type object struct {
		index map[*privateCopyNode]*privateCopyNode
		node  *privateCopyNode
	}
	node := &privateCopyNode{value: 1}
	node.next = node
	src := &object{index: map[*privateCopyNode]*privateCopyNode{node: node, nil: nil}, node: node}
	got := resource.DeepCopyStruct(src).(*object)
	if len(got.index) != 2 || got.index[got.node] != got.node || got.node.next != got.node {
		t.Fatal("map key/value identity or cycle was lost")
	}
	if _, exists := got.index[src.node]; exists {
		t.Fatal("map retained the source pointer key")
	}
	if value, exists := got.index[nil]; !exists || value != nil {
		t.Fatal("nil map entry was lost")
	}
}

func TestDeepCopyStructRootInsideOwner(t *testing.T) {
	t.Parallel()
	type inner struct {
		owner any
		data  []byte
	}
	type owner struct {
		first *inner
		inner inner
		array [2]*inner
	}
	src := &owner{}
	src.first = &src.inner
	src.inner = inner{owner: src, data: []byte{1}}
	src.array = [2]*inner{&src.inner, &src.inner}
	got := resource.DeepCopyStruct(&src.inner).(*inner)
	parent := got.owner.(*owner)
	if &parent.inner != got || parent.first != got || parent.array[0] != got || parent.array[1] != got {
		t.Fatal("root inside a larger reachable object lost its identity")
	}
	got.data[0] = 9
	if src.inner.data[0] != 1 {
		t.Fatal("nested root still shares source storage")
	}
}

func TestDeepCopyStructBigInt(t *testing.T) {
	t.Parallel()
	type object struct {
		value big.Int
		items map[string]big.Int
	}
	var value big.Int
	value.Lsh(big.NewInt(1), 130)
	src := &object{value: value, items: map[string]big.Int{"key": value}}
	first := resource.DeepCopyStruct(src).(*object)
	second := resource.DeepCopyStruct(src).(*object)
	first.value.SetBit(&first.value, 0, 1)
	item := first.items["key"]
	item.SetBit(&item, 1, 1)
	for _, obj := range []*object{src, second} {
		item := obj.items["key"]
		if obj.value.Bit(0) != 0 || obj.value.Bit(1) != 0 || item.Bit(0) != 0 || item.Bit(1) != 0 {
			t.Fatal("big.Int private storage was shared across copies")
		}
		if obj.value.Bit(130) != 1 || item.Bit(130) != 1 {
			t.Fatal("big.Int value was lost")
		}
	}
}

func TestDeepCopyStructSliceLengthAndCapacity(t *testing.T) {
	for _, length := range []int{0, 2} {
		name := "empty"
		if length > 0 {
			name = "nonempty"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			type object struct {
				data  json.RawMessage
				nodes []*privateCopyNode
			}
			data := json.RawMessage{1, 2, 3, 4, 5}
			nodes := []*privateCopyNode{
				{value: 1}, {value: 2}, {value: 3}, {value: 4}, {value: 5},
			}
			src := &object{data: data[:length], nodes: nodes[:length]}
			got := resource.DeepCopyStruct(src).(*object)
			if len(got.data) != length || cap(got.data) != 5 || len(got.nodes) != length || cap(got.nodes) != 5 {
				t.Fatal("slice length or capacity changed")
			}
			if !slices.Equal(got.data, src.data) {
				t.Fatal("visible bytes changed")
			}
			for i := range length {
				if got.nodes[i] == src.nodes[i] || got.nodes[i].value != src.nodes[i].value {
					t.Fatal("visible pointer element was not copied")
				}
			}
			for i := length; i < cap(got.data); i++ {
				if got.data[:cap(got.data)][i] != 0 || got.nodes[:cap(got.nodes)][i] != nil {
					t.Fatal("elements beyond len were copied")
				}
			}
			got.data = append(got.data, 9)
			got.nodes = append(got.nodes, &privateCopyNode{value: 9})
			if data[length] != byte(length+1) || nodes[length].value != length+1 {
				t.Fatal("append changed source storage")
			}
		})
	}
}

func TestDeepCopyStructDistinctSliceViews(t *testing.T) {
	t.Parallel()
	type object struct {
		short []int
		long  []int
		tight []int
	}
	data := []int{1, 2, 3, 4}
	src := &object{short: data[:1], long: data[:3], tight: data[:3:3]}
	got := resource.DeepCopyStruct(src).(*object)
	for name, pair := range map[string][2][]int{
		"short": {src.short, got.short}, "long": {src.long, got.long}, "tight": {src.tight, got.tight},
	} {
		if !slices.Equal(pair[0], pair[1]) || cap(pair[0]) != cap(pair[1]) {
			t.Errorf("%s view lost its length, capacity or values", name)
		}
		for _, value := range pair[1][len(pair[1]):cap(pair[1])] {
			if value != 0 {
				t.Errorf("%s view copied elements beyond len", name)
			}
		}
	}
	got.long[0] = 9
	if data[0] != 1 {
		t.Fatal("slice view still uses source storage")
	}
}

func TestDeepCopyStructTime(t *testing.T) {
	tests := []struct {
		name string
		time time.Time
	}{
		{name: "zero"},
		{name: "monotonic", time: time.Now()},
		{name: "timezone", time: time.Date(2026, 10, 20, 12, 30, 0, 123, time.FixedZone("custom", 3*60*60))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			type object struct {
				at         time.Time
				conditions []conditions.Condition
			}
			src := &object{at: tt.time, conditions: []conditions.Condition{{LastTransitionTime: tt.time}}}
			got := resource.DeepCopyStruct(src).(*object)
			if got.at != tt.time || got.conditions[0].LastTransitionTime != tt.time {
				t.Fatal("time value, location or monotonic data changed")
			}
			got.at = got.at.Add(time.Hour)
			got.conditions[0].LastTransitionTime = got.at
			if src.at != tt.time || src.conditions[0].LastTransitionTime != tt.time {
				t.Fatal("time mutation escaped the copy")
			}
		})
	}
}

func TestDeepCopyStructInterfaceNilAndMapValues(t *testing.T) {
	t.Parallel()
	type object struct {
		missing any
		typed   any
		values  map[string]json.RawMessage
		numbers map[float64]json.RawMessage
	}
	src := &object{
		typed:   (*privateCopyNode)(nil),
		values:  map[string]json.RawMessage{"nil": nil, "empty": {}},
		numbers: map[float64]json.RawMessage{math.NaN(): json.RawMessage(`[1]`)},
	}
	got := resource.DeepCopyStruct(src).(*object)
	if got.missing != nil || got.typed == nil || got.typed.(*privateCopyNode) != nil {
		t.Fatal("nil interface or typed nil changed")
	}
	if value, exists := got.values["nil"]; !exists || value != nil {
		t.Fatal("nil RawMessage map value changed")
	}
	if value, exists := got.values["empty"]; !exists || value == nil || len(value) != 0 {
		t.Fatal("empty RawMessage map value changed")
	}
	if len(got.numbers) != 1 {
		t.Fatal("NaN map key was lost")
	}
	for _, value := range got.numbers {
		if string(value) != "[1]" {
			t.Fatal("NaN map value was lost")
		}
		value[1] = '2'
	}
	for _, value := range src.numbers {
		if string(value) != "[1]" {
			t.Fatal("map value shares source storage")
		}
	}
}

func TestDeepCopyStructConcurrentFirstUse(t *testing.T) {
	// A local type used only here exercises concurrent plan publication in a
	// fresh test process. Every invocation must have its own cycle table.
	type object struct {
		value int
		data  json.RawMessage
		next  *object
	}
	src := &object{value: 1, data: json.RawMessage(`[1]`)}
	src.next = src
	const workers = 16
	copies := make([]*object, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 20 {
				got := resource.DeepCopyStruct(src).(*object)
				got.next.value = i + 2
				got.data[1] = '2'
				copies[i] = got
			}
		}()
	}
	close(start)
	wg.Wait()
	if src.value != 1 || string(src.data) != "[1]" {
		t.Fatal("concurrent copies changed the source")
	}
	for i, got := range copies {
		if got.next != got || got.value != i+2 || string(got.data) != "[2]" {
			t.Fatalf("worker %d received a shared or incomplete copy", i)
		}
	}
}

func TestDeepCopyStructInputContract(t *testing.T) {
	t.Parallel()
	type object struct{ Value int }
	type pointer *object
	src := pointer(&object{Value: 1})
	got := resource.DeepCopyStruct(src)
	if reflect.TypeOf(got) != reflect.TypeOf(src) || got.(pointer) == src {
		t.Fatal("named pointer type or isolation was lost")
	}
	valid := []struct {
		name  string
		value any
	}{
		{name: "named_struct", value: object{Value: 7}},
		{name: "anonymous_struct", value: struct{ Value int }{Value: 7}},
		{name: "empty_struct", value: struct{}{}},
		{name: "pointer", value: &object{Value: 7}},
	}
	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			got := resource.DeepCopyStruct(tt.value)
			if reflect.TypeOf(got) != reflect.TypeOf(tt.value) || !reflect.DeepEqual(got, tt.value) {
				t.Fatal("copy changed the input type or value")
			}
		})
	}
	tests := []struct {
		name  string
		value any
	}{
		{name: "nil"},
		{name: "typed_nil", value: (*object)(nil)},
		{name: "named_typed_nil", value: pointer(nil)},
		{name: "number", value: 7},
		{name: "slice", value: []byte{}},
		{name: "map", value: map[string]int{}},
		{name: "array", value: [1]int{7}},
		{name: "slice_pointer", value: &[]byte{}},
		{name: "pointer_to_pointer", value: &src},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("invalid input did not panic")
				}
			}()
			resource.DeepCopyStruct(tt.value)
		})
	}
}
