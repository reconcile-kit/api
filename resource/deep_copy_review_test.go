package resource_test

import (
	"testing"

	"github.com/reconcile-kit/api/resource"
)

func TestReviewDeepCopyZeroSizeMapKeys(t *testing.T) {
	t.Parallel()
	type slot struct {
		key struct{}
		pad byte
	}
	type object struct {
		slots [2]slot
		index map[*struct{}]int
	}
	src := &object{}
	src.index = map[*struct{}]int{&src.slots[0].key: 1, &src.slots[1].key: 2}
	if len(src.index) != 2 {
		t.Fatal("fixture must have two distinct keys")
	}
	got := resource.DeepCopyStruct(src).(*object)
	if len(got.index) != 2 {
		t.Fatalf("copied map has %d entries, want 2: %v", len(got.index), got.index)
	}
}

func TestReviewDeepCopyArraySliceOwner(t *testing.T) {
	t.Parallel()
	type object struct {
		array [2]int
		view  []int
		elem  *int
	}
	src := &object{array: [2]int{1, 2}}
	src.view = src.array[:]
	src.elem = &src.view[1]
	got := resource.DeepCopyStruct(src).(*object)
	if got.elem != &got.view[1] {
		t.Errorf("element pointer = %p, copied slice element = %p; want the same address", got.elem, &got.view[1])
	}
	*got.elem = 9
	if got.view[1] != 9 {
		t.Errorf("slice element = %d after writing through its pointer, want 9", got.view[1])
	}
	if src.array[1] != 2 {
		t.Fatal("copy changed source")
	}
}
