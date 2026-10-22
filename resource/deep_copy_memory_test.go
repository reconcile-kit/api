//go:build memorycheck

package resource

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"testing"
	"unsafe"
	"weak"
)

// Run explicitly with -tags=memorycheck. Each case gets a fresh process so
// other tests, earlier -count iterations and warmed reflect caches cannot hide
// retention. Heap measurements are diagnostic; exact cache cardinality and
// reachability checks are assertions, not machine-dependent byte thresholds.
func TestDeepCopyStructCacheLifetime(t *testing.T) {
	if scenario := os.Getenv("DEEPCOPY_MEMORY_CHILD"); scenario != "" {
		runCopyMemoryScenario(t, scenario)
		return
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"ordinary_graphs", "repeating_unions", "changing_union_layouts"} {
		t.Run(scenario, func(t *testing.T) {
			cmd := exec.Command(binary, "-test.run=^TestDeepCopyStructCacheLifetime$", "-test.v", "-test.timeout=120s")
			cmd.Env = append(os.Environ(), "DEEPCOPY_MEMORY_CHILD="+scenario, "GOMAXPROCS=1")
			output, err := cmd.CombinedOutput()
			t.Logf("%s", output)
			if err != nil {
				t.Fatalf("isolated memory check: %v", err)
			}
		})
	}
}

type copyMemorySample struct {
	Phase       string
	Plans       int
	HeapBytes   uint64
	HeapObjects uint64
	TotalAlloc  uint64
	Mallocs     uint64
}

func sampleCopyMemory(phase string) copyMemorySample {
	runtime.GC()
	runtime.GC()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	sample := copyMemorySample{Phase: phase, HeapBytes: memory.HeapAlloc, HeapObjects: memory.HeapObjects, TotalAlloc: memory.TotalAlloc, Mallocs: memory.Mallocs}
	copyPlans.Range(func(_, _ any) bool { sample.Plans++; return true })
	return sample
}

func runCopyMemoryScenario(t *testing.T, scenario string) {
	// Record small retained type allocations as well as large backing arrays.
	runtime.MemProfileRate = 64
	var exercise func(*testing.T, int) []copyMemoryWeak
	switch scenario {
	case "ordinary_graphs":
		exercise = exerciseCopyMemoryGraph
	case "repeating_unions":
		exercise = exerciseCopyMemoryRepeatedUnion
	case "changing_union_layouts":
		exercise = exerciseCopyMemoryMixedUnion
	default:
		t.Fatalf("unknown scenario %q", scenario)
	}
	batch := func(first, last int) {
		var probes []copyMemoryWeak
		for seed := first; seed <= last; seed++ {
			probes = append(probes, exercise(t, seed)...)
		}
		runtime.GC()
		runtime.GC()
		for _, probe := range probes {
			if probe.graph.Value() != nil || probe.node.Value() != nil || probe.buffer.Value() != nil || probe.union.Value() != nil || probe.window.Value() != nil || probe.payload.Value() != nil || probe.pointerArray.Value() != nil {
				t.Fatal("a source or copied object is still retained after dropping the graph")
			}
		}
	}
	batch(2, 32)
	samples := []copyMemorySample{sampleCopyMemory("warm_2_to_32")}
	for _, limit := range []int{128, 256, 512} {
		first := 33
		if limit > 128 {
			first = limit/2 + 1
		}
		batch(first, limit)
		phase := "new_through_" + strconv.Itoa(limit)
		samples = append(samples, sampleCopyMemory(phase))
	}
	for round := range 3 {
		batch(2, 512)
		samples = append(samples, sampleCopyMemory("repeat_"+strconv.Itoa(round+1)))
	}
	for _, sample := range samples {
		t.Logf("%s: plans=%d heap_bytes=%d heap_objects=%d total_alloc=%d mallocs=%d", sample.Phase, sample.Plans, sample.HeapBytes, sample.HeapObjects, sample.TotalAlloc, sample.Mallocs)
		if sample.Plans != samples[0].Plans {
			t.Errorf("same concrete types added copyPlans entries: warm=%d phase=%s count=%d", samples[0].Plans, sample.Phase, sample.Plans)
		}
	}
	if directory := os.Getenv("DEEPCOPY_MEMORY_OUTPUT"); directory != "" {
		data, err := json.MarshalIndent(samples, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, scenario+".json"), append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		profile, err := os.Create(filepath.Join(directory, scenario+".heap"))
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.WriteHeapProfile(profile); err != nil {
			t.Fatal(err)
		}
		if err := profile.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

type copyMemoryPayload [128]byte
type copyMemoryNode struct {
	ID   int
	Next *copyMemoryNode
	Peer *copyMemoryNode
	Data []byte
}
type copyMemoryGraph struct {
	Buffer []byte
	Alias  []byte
	Nodes  []*copyMemoryNode
	Index  map[*copyMemoryNode]int
	Self   *copyMemoryGraph
}

type copyMemoryWeak struct {
	graph        weak.Pointer[copyMemoryGraph]
	node         weak.Pointer[copyMemoryNode]
	buffer       weak.Pointer[byte]
	union        weak.Pointer[copyMemoryUnionGraph]
	window       weak.Pointer[copyMemoryWindow]
	payload      weak.Pointer[copyMemoryPayload]
	pointerArray weak.Pointer[[2]*copyMemoryPayload]
}

//go:noinline
func exerciseCopyMemoryGraph(t *testing.T, seed int) []copyMemoryWeak {
	count := 2 + seed%31
	capacity := 128 + seed*13
	length := 1 + seed%capacity
	src := &copyMemoryGraph{Buffer: make([]byte, length, capacity), Nodes: make([]*copyMemoryNode, count), Index: make(map[*copyMemoryNode]int)}
	src.Alias, src.Self = src.Buffer, src
	src.Buffer[0] = byte(seed)
	for i := range src.Nodes {
		src.Nodes[i] = &copyMemoryNode{ID: i, Data: make([]byte, 1+i%7, 128+i+seed)}
		src.Index[src.Nodes[i]] = i + 1
	}
	for i, node := range src.Nodes {
		node.Next = src.Nodes[(i+seed)%count]
		node.Peer = src.Nodes[(i*3+seed/3)%count]
	}
	first, second := DeepCopyStruct(src).(*copyMemoryGraph), DeepCopyStruct(src).(*copyMemoryGraph)
	for _, got := range []*copyMemoryGraph{first, second} {
		if got.Self != got || &got.Buffer[0] != &got.Alias[0] || got.Buffer[0] != byte(seed) || len(got.Buffer) != length || cap(got.Buffer) != capacity {
			t.Fatal("ordinary graph shape or values changed")
		}
		for i, node := range got.Nodes {
			if node == src.Nodes[i] || node.ID != i || got.Index[node] != i+1 || node.Next != got.Nodes[(i+seed)%count] || node.Peer != got.Nodes[(i*3+seed/3)%count] {
				t.Fatal("ordinary graph aliases changed")
			}
		}
		for _, b := range got.Buffer[len(got.Buffer):cap(got.Buffer)] {
			if b != 0 {
				t.Fatal("spare capacity is nonzero")
			}
		}
	}
	first.Buffer[0]++
	first.Nodes[0].Data[0] = 99
	if src.Buffer[0] != byte(seed) || second.Buffer[0] != byte(seed) || src.Nodes[0].Data[0] != 0 || second.Nodes[0].Data[0] != 0 {
		t.Fatal("ordinary graph copies share mutable data")
	}
	var probes []copyMemoryWeak
	for _, graph := range []*copyMemoryGraph{src, first, second} {
		probes = append(probes, copyMemoryWeak{graph: weak.Make(graph), buffer: weak.Make(&graph.Buffer[0])})
		for _, node := range graph.Nodes {
			probes = append(probes, copyMemoryWeak{node: weak.Make(node), buffer: weak.Make(&node.Data[0])})
		}
	}
	return probes
}

type copyMemoryBlock struct {
	Number  uintptr
	Pointer *copyMemoryPayload
}
type copyMemoryWindow struct {
	Block copyMemoryBlock
	Last  uintptr
}
type copyMemoryBridge struct {
	Pointer *copyMemoryPayload
	Number  uintptr
}
type copyMemoryUnionGraph struct {
	Windows  []*copyMemoryWindow
	Pointers []*[2]*copyMemoryPayload
	Numbers  []*[2]uintptr
	Bridge   *copyMemoryBridge
}

//go:noinline
func exerciseCopyMemoryRepeatedUnion(t *testing.T, count int) []copyMemoryWeak {
	backing := make([]copyMemoryBlock, count+1)
	payload := &copyMemoryPayload{7}
	src := &copyMemoryUnionGraph{Windows: make([]*copyMemoryWindow, count)}
	for i := range backing {
		backing[i] = copyMemoryBlock{Number: uintptr(i + 1), Pointer: payload}
	}
	for i := range src.Windows {
		src.Windows[i] = (*copyMemoryWindow)(unsafe.Pointer((*[2]copyMemoryBlock)(backing[i:])))
	}
	got := DeepCopyStruct(src).(*copyMemoryUnionGraph)
	for i, window := range got.Windows {
		if window.Block.Number != uintptr(i+1) || window.Last != uintptr(i+2) || window.Block.Pointer == payload || window.Block.Pointer[0] != 7 {
			t.Fatal("repeating union lost values or isolation")
		}
		if i+1 < count && &window.Last != &got.Windows[i+1].Block.Number {
			t.Fatal("repeating union lost overlap")
		}
	}
	probes := []copyMemoryWeak{{union: weak.Make(src), window: weak.Make(src.Windows[0]), payload: weak.Make(payload)}, {union: weak.Make(got), window: weak.Make(got.Windows[0]), payload: weak.Make(got.Windows[0].Block.Pointer)}}
	runtime.KeepAlive(backing)
	return probes
}

// The backing type is fixed for all inputs. No reflect type factories are used
// by this fixture. Only lengths and connections between fixed-size views vary.
type copyMemoryMixedBacking struct {
	Pointers [512]*copyMemoryPayload
	Numbers  [512]uintptr
}

//go:noinline
func exerciseCopyMemoryMixedUnion(t *testing.T, count int) []copyMemoryWeak {
	backing := new(copyMemoryMixedBacking)
	payload := &copyMemoryPayload{7}
	for i := range backing.Pointers {
		backing.Pointers[i] = payload
		backing.Numbers[i] = uintptr(i + 1)
	}
	src := &copyMemoryUnionGraph{}
	for i := 512 - count; i < 511; i++ {
		src.Pointers = append(src.Pointers, (*[2]*copyMemoryPayload)(backing.Pointers[i:]))
	}
	for i := 0; i < count-1; i++ {
		src.Numbers = append(src.Numbers, (*[2]uintptr)(backing.Numbers[i:]))
	}
	// The bridge spans the last pointer and the first integer in one typed
	// allocation, with the same GC layout and alignment as those two fields.
	offset := unsafe.Offsetof(backing.Numbers) - unsafe.Sizeof(payload)
	src.Bridge = (*copyMemoryBridge)(unsafe.Add(unsafe.Pointer(backing), offset))
	got := DeepCopyStruct(src).(*copyMemoryUnionGraph)
	if &got.Pointers[len(got.Pointers)-1][1] != &got.Bridge.Pointer || &got.Numbers[0][0] != &got.Bridge.Number {
		t.Fatal("mixed union lost its bridge")
	}
	for i, view := range got.Pointers {
		if view[0] == payload || view[0][0] != 7 || view[1] != view[0] {
			t.Fatal("mixed union lost pointer values or isolation")
		}
		if i+1 < len(got.Pointers) && &view[1] != &got.Pointers[i+1][0] {
			t.Fatal("mixed pointer views lost overlap")
		}
	}
	for i, view := range got.Numbers {
		if view[0] != uintptr(i+1) || view[1] != uintptr(i+2) {
			t.Fatal("mixed union lost scalar values")
		}
		if i+1 < len(got.Numbers) && &view[1] != &got.Numbers[i+1][0] {
			t.Fatal("mixed scalar views lost overlap")
		}
	}
	got.Bridge.Number = 99
	if backing.Numbers[0] != 1 || got.Numbers[0][0] != 99 {
		t.Fatal("mixed union mutation lost aliasing or isolation")
	}
	probes := []copyMemoryWeak{{union: weak.Make(src), pointerArray: weak.Make(src.Pointers[0]), payload: weak.Make(payload)}, {union: weak.Make(got), pointerArray: weak.Make(got.Pointers[0]), payload: weak.Make(got.Pointers[0][0])}}
	runtime.KeepAlive(backing)
	return probes
}
