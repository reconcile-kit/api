package resource_test

import (
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/reconcile-kit/api/resource"
)

// Keep the fixture on the original consumer API: no generated copy methods.
type copyResource struct {
	resource.Resource
	Spec     json.RawMessage
	Status   json.RawMessage
	Bytes    []byte
	Matrix   [][]byte
	Entries  []copyEntry
	Pointers []*copyEntry
	Messages []json.RawMessage
	ByName   map[string]json.RawMessage
}

type copyEntry struct {
	Data json.RawMessage
}

func (r *copyResource) DeepCopy() *copyResource {
	return resource.DeepCopyStruct(r).(*copyResource)
}

func newCopyResource() *copyResource {
	return &copyResource{
		Resource: resource.Resource{
			Name:        "original",
			Labels:      map[string]string{"team": "original"},
			Annotations: map[string]string{"owner": "original"},
			Finalizers:  []string{"original"},
		},
		Spec:     json.RawMessage(`[1]`),
		Status:   json.RawMessage(`[1]`),
		Bytes:    []byte(`[1]`),
		Matrix:   [][]byte{[]byte(`[1]`)},
		Entries:  []copyEntry{{Data: json.RawMessage(`[1]`)}},
		Pointers: []*copyEntry{{Data: json.RawMessage(`[1]`)}},
		Messages: []json.RawMessage{json.RawMessage(`[1]`)},
		ByName:   map[string]json.RawMessage{"item": json.RawMessage(`[1]`)},
	}
}

type copyBufferCase struct {
	name   string
	buffer func(*copyResource) []byte
}

func copyBufferCases() []copyBufferCase {
	return []copyBufferCase{
		{name: "byte_slice", buffer: func(r *copyResource) []byte { return r.Bytes }},
		{name: "raw_message_spec", buffer: func(r *copyResource) []byte { return r.Spec }},
		{name: "raw_message_status", buffer: func(r *copyResource) []byte { return r.Status }},
		{name: "nested_slice", buffer: func(r *copyResource) []byte { return r.Matrix[0] }},
		{name: "struct_slice", buffer: func(r *copyResource) []byte { return r.Entries[0].Data }},
		{name: "pointer_slice", buffer: func(r *copyResource) []byte { return r.Pointers[0].Data }},
		{name: "raw_message_slice", buffer: func(r *copyResource) []byte { return r.Messages[0] }},
		{name: "raw_message_map", buffer: func(r *copyResource) []byte { return r.ByName["item"] }},
	}
}

// These are regression tests for the required isolation, so they must fail
// while DeepCopyStruct still shares mutable data between copies.
func TestDeepCopyStructBufferIsolation(t *testing.T) {
	for _, tt := range copyBufferCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := newCopyResource()
			first, second := src.DeepCopy(), src.DeepCopy()
			if first == src || second == src || first == second {
				t.Fatal("copies must be distinct objects")
			}
			if got := string(tt.buffer(first)); got != "[1]" {
				t.Fatalf("initial copy = %q, want [1]", got)
			}
			if got := string(tt.buffer(second)); got != "[1]" {
				t.Fatalf("initial second copy = %q, want [1]", got)
			}

			tt.buffer(first)[1] = '2'
			tt.buffer(second)[1] = '3'
			if got := string(tt.buffer(src)); got != "[1]" {
				t.Errorf("source changed: got %q, want [1]", got)
			}
			if got := string(tt.buffer(first)); got != "[2]" {
				t.Errorf("first copy changed through second: got %q, want [2]", got)
			}
			if got := string(tt.buffer(second)); got != "[3]" {
				t.Errorf("second copy = %q, want [3]", got)
			}
		})
	}
}

func TestDeepCopyStructMetadataIsolation(t *testing.T) {
	t.Parallel()
	src := newCopyResource()
	first, second := src.DeepCopy(), src.DeepCopy()

	first.Labels["team"] = "first"
	first.Annotations["owner"] = "first"
	first.Finalizers[0] = "first"
	delete(first.Labels, "team")
	first.Labels["new"] = "first"

	for name, obj := range map[string]*copyResource{"source": src, "second": second} {
		if got := obj.Labels["team"]; got != "original" {
			t.Errorf("%s label = %q, want original", name, got)
		}
		if _, ok := obj.Labels["new"]; ok {
			t.Errorf("%s received a key inserted into the first copy", name)
		}
		if got := obj.Annotations["owner"]; got != "original" {
			t.Errorf("%s annotation = %q, want original", name, got)
		}
		if got := obj.Finalizers[0]; got != "original" {
			t.Errorf("%s finalizer = %q, want original", name, got)
		}
	}
}

func TestDeepCopyStructAppendSpareCapacity(t *testing.T) {
	tests := []struct {
		name   string
		length int
	}{
		{name: "empty", length: 0},
		{name: "nonempty", length: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			byteBacking := []byte{'1', 'x', 'x'}
			rawBacking := json.RawMessage{'1', 'x', 'x'}
			src := &copyResource{Bytes: byteBacking[:tt.length], Spec: rawBacking[:tt.length]}
			first, second := src.DeepCopy(), src.DeepCopy()

			first.Bytes = append(first.Bytes, '2')
			second.Bytes = append(second.Bytes, '3')
			first.Spec = append(first.Spec, ' ')
			second.Spec = append(second.Spec, '\n')

			if !slices.Equal(byteBacking, []byte{'1', 'x', 'x'}) {
				t.Errorf("append changed source byte backing array: %q", byteBacking)
			}
			if !slices.Equal(rawBacking, json.RawMessage{'1', 'x', 'x'}) {
				t.Errorf("append changed source RawMessage backing array: %q", rawBacking)
			}
			if first.Bytes[tt.length] != '2' || second.Bytes[tt.length] != '3' {
				t.Error("byte slices share appended elements")
			}
			if first.Spec[tt.length] != ' ' || second.Spec[tt.length] != '\n' {
				t.Error("RawMessage copies share appended elements")
			}
		})
	}
}

func TestDeepCopyStructRawMessageUnmarshal(t *testing.T) {
	t.Parallel()
	src := newCopyResource()
	first, second := src.DeepCopy(), src.DeepCopy()

	// Call RawMessage's method directly: it can reuse its existing byte buffer.
	if err := first.Spec.UnmarshalJSON([]byte(`[2]`)); err != nil {
		t.Fatal(err)
	}
	if err := second.Spec.UnmarshalJSON([]byte(`[3]`)); err != nil {
		t.Fatal(err)
	}
	if got := string(src.Spec); got != "[1]" {
		t.Errorf("source RawMessage = %q, want [1]", got)
	}
	if got := string(first.Spec); got != "[2]" {
		t.Errorf("first RawMessage = %q, want [2]", got)
	}
	if got := string(second.Spec); got != "[3]" {
		t.Errorf("second RawMessage = %q, want [3]", got)
	}
}

// Control case: the same buffers must also copy correctly in an unnamed root.
// This distinguishes a RawMessage issue from skipping named struct fields.
func TestDeepCopyStructAnonymousRoot(t *testing.T) {
	t.Parallel()
	src := &struct {
		Bytes []byte
		Raw   json.RawMessage
	}{Bytes: []byte(`[1]`), Raw: json.RawMessage(`[1]`)}
	first := resource.DeepCopyStruct(src).(*struct {
		Bytes []byte
		Raw   json.RawMessage
	})
	second := resource.DeepCopyStruct(src).(*struct {
		Bytes []byte
		Raw   json.RawMessage
	})

	first.Bytes[1], first.Raw[1] = '2', '2'
	second.Bytes[1], second.Raw[1] = '3', '3'
	if string(src.Bytes) != "[1]" || string(src.Raw) != "[1]" {
		t.Fatal("source buffers changed")
	}
	if string(first.Bytes) != "[2]" || string(first.Raw) != "[2]" {
		t.Fatal("first copy buffers changed through the second copy")
	}
	if string(second.Bytes) != "[3]" || string(second.Raw) != "[3]" {
		t.Fatal("second copy lost its mutations")
	}
}

func TestDeepCopyStructNestedNamedStruct(t *testing.T) {
	t.Parallel()
	src := &struct{ Entry copyEntry }{Entry: copyEntry{Data: json.RawMessage(`[1]`)}}
	first := resource.DeepCopyStruct(src).(*struct{ Entry copyEntry })
	second := resource.DeepCopyStruct(src).(*struct{ Entry copyEntry })

	first.Entry.Data[1] = '2'
	second.Entry.Data[1] = '3'
	if got := string(src.Entry.Data); got != "[1]" {
		t.Errorf("source nested RawMessage = %q, want [1]", got)
	}
	if got := string(first.Entry.Data); got != "[2]" {
		t.Errorf("first nested RawMessage = %q, want [2]", got)
	}
	if got := string(second.Entry.Data); got != "[3]" {
		t.Errorf("second nested RawMessage = %q, want [3]", got)
	}
}

func TestDeepCopyStructPreservesNilAndEmpty(t *testing.T) {
	tests := []struct {
		name string
		src  *copyResource
	}{
		{name: "nil", src: &copyResource{}},
		{name: "empty", src: &copyResource{Bytes: []byte{}, Spec: json.RawMessage{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.src.DeepCopy()
			if (got.Bytes == nil) != (tt.src.Bytes == nil) || len(got.Bytes) != 0 {
				t.Errorf("byte slice nil/empty state changed: %#v", got.Bytes)
			}
			if (got.Spec == nil) != (tt.src.Spec == nil) || len(got.Spec) != 0 {
				t.Errorf("RawMessage nil/empty state changed: %#v", got.Spec)
			}
		})
	}
}

func TestDeepCopyStructConcurrentCopies(t *testing.T) {
	// Do not parallelize these subtests: each race report should identify one
	// buffer shape. All copies finish before either worker starts mutating.
	for _, tt := range copyBufferCases() {
		t.Run(tt.name, func(t *testing.T) {
			src := newCopyResource()
			first, second := src.DeepCopy(), src.DeepCopy()
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i, obj := range []*copyResource{first, second} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					buf := tt.buffer(obj)
					for range 1000 {
						buf[1] = byte('2' + i)
					}
				}()
			}
			close(start)
			wg.Wait()

			if got := string(tt.buffer(src)); got != "[1]" {
				t.Errorf("source changed: got %q, want [1]", got)
			}
			if got := string(tt.buffer(first)); got != "[2]" {
				t.Errorf("first copy = %q, want [2]", got)
			}
			if got := string(tt.buffer(second)); got != "[3]" {
				t.Errorf("second copy = %q, want [3]", got)
			}
		})
	}
}
