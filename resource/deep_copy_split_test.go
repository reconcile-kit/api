package resource_test

import (
	"runtime"
	"sync"
	"testing"
	"unsafe"

	"github.com/reconcile-kit/api/resource"
)

func TestDeepCopyStructSplitsOpaquePointerViews(t *testing.T) {
	type typed struct{ Pointer *int }
	type opaque struct{ Pointer unsafe.Pointer }
	type graph struct {
		Views  []any
		Typed  *typed
		Opaque *opaque
		Field  **int
		Raw    *unsafe.Pointer
		Index  map[*typed]*opaque
		Self   *graph
	}
	for _, order := range []string{"typed_first", "opaque_first"} {
		for _, form := range []string{"pointer", "value"} {
			t.Run(order+"/"+form, func(t *testing.T) {
				t.Parallel()
				number := 7
				holder := &typed{Pointer: &number}
				raw := (*opaque)(unsafe.Pointer(holder))
				src := &graph{Typed: holder, Opaque: raw, Field: &holder.Pointer, Raw: &raw.Pointer, Index: map[*typed]*opaque{holder: raw}}
				src.Self = src
				src.Views = []any{holder, raw}
				if order == "opaque_first" {
					src.Views[0], src.Views[1] = src.Views[1], src.Views[0]
				}
				clone := func() *graph {
					if form == "value" {
						got := resource.DeepCopyStruct(*src).(graph)
						return &got
					}
					return resource.DeepCopyStruct(src).(*graph)
				}
				first, second := clone(), clone()
				for _, got := range []*graph{first, second} {
					if unsafe.Pointer(got.Typed) == unsafe.Pointer(got.Opaque) {
						t.Error("conflicting pointer representations still share storage")
					}
					if got.Typed.Pointer == holder.Pointer || *got.Typed.Pointer != 7 {
						t.Error("typed pointer lost value or retained source data")
					}
					if got.Opaque.Pointer != raw.Pointer {
						t.Error("opaque pointer identity changed")
					}
					if got.Field != &got.Typed.Pointer || got.Raw != &got.Opaque.Pointer || got.Index[got.Typed] != got.Opaque {
						t.Error("compatible field references or map keys lost their owners")
					}
					if got.Self == src || got.Self.Self != got.Self || got.Self.Typed != got.Typed || got.Self.Opaque != got.Opaque || (form == "pointer") != (got.Self == got) {
						t.Error("split views lost cycles or root semantics")
					}
					for _, view := range got.Views {
						switch view := view.(type) {
						case *typed:
							if view != got.Typed {
								t.Error("typed view was duplicated")
							}
						case *opaque:
							if view != got.Opaque {
								t.Error("opaque view was duplicated")
							}
						}
					}
				}
				if t.Failed() {
					return
				}
				var wg sync.WaitGroup
				for i, got := range []*graph{first, second} {
					wg.Add(1)
					go func() { defer wg.Done(); *got.Typed.Pointer = 10 + i; *got.Raw = nil }()
				}
				wg.Wait()
				if number != 7 || holder.Pointer != &number || raw.Pointer != unsafe.Pointer(&number) || *first.Typed.Pointer != 10 || *second.Typed.Pointer != 11 {
					t.Fatal("split view mutations escaped their copies")
				}
				runtime.KeepAlive(src)
			})
		}
	}
}
