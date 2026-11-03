package resource_test

import (
	"encoding/json"
	"testing"

	"github.com/reconcile-kit/api/resource"
)

func BenchmarkDeepCopyStruct(b *testing.B) {
	type rawObject struct {
		Spec json.RawMessage
	}
	cycle := &privateCopyNode{value: 1, data: json.RawMessage(`[1]`)}
	cycle.next = cycle
	cases := []struct {
		name string
		src  any
	}{
		{name: "resource", src: newCopyResource()},
		{name: "raw_len64_cap64", src: &rawObject{Spec: make(json.RawMessage, 64)}},
		{name: "raw_len64_cap4096", src: &rawObject{Spec: make(json.RawMessage, 64, 4096)}},
		{name: "raw_64KiB", src: &rawObject{Spec: make(json.RawMessage, 64*1024)}},
		{name: "cycle", src: cycle},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			// Measure steady-state copying after type metadata has been cached.
			resource.DeepCopyStruct(tc.src)
			b.ReportAllocs()
			for b.Loop() {
				resource.DeepCopyStruct(tc.src)
			}
		})
	}
}
