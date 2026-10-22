//go:build memorycheck

package resource

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"slices"
	"testing"
	"unsafe"
	"weak"
)

// These diagnostics deliberately do not assert a machine-dependent heap limit.
// Run each scenario in a fresh process; PASS means graph/GC assertions passed,
// not that the measured retained-metadata growth is acceptable.
func TestDeepCopyAuditMemory(t *testing.T) {
	scenario := os.Getenv("DEEPCOPY_AUDIT_SCENARIO")
	if scenario == "" {
		t.Skip("set DEEPCOPY_AUDIT_SCENARIO; use a fresh process per scenario")
	}
	runtime.MemProfileRate = 64
	var exercise func(*testing.T, int)
	switch scenario {
	case "multirun":
		exercise = func(t *testing.T, n int) { auditExerciseUnion(t, n, true) }
	case "multirun_control":
		exercise = func(t *testing.T, n int) { auditExerciseUnion(t, n, false) }
	case "alignment":
		exercise = auditExerciseAlignment
	case "alignment_fallback":
		exercise = auditExerciseAlignmentFallback
	case "dynamic_types":
		exercise = func(t *testing.T, n int) { auditExerciseDynamic(t, n, true) }
	case "dynamic_control":
		exercise = func(t *testing.T, n int) { auditExerciseDynamic(t, n, false) }
	case "graphs":
		exercise = auditExerciseGraph
	default:
		t.Fatalf("unknown scenario %q", scenario)
	}
	// Warm JSON metadata before the baseline; measurement records have fixed size.
	_, _ = json.Marshal(auditSample{})
	samples := make([]auditSample, 0, 7)
	batch := func(first, last int) {
		for n := first; n <= last; n++ {
			exercise(t, n)
		}
	}
	batch(1, 16)
	samples = append(samples, auditTakeSample("warm_16"))
	for _, span := range [][2]int{{17, 64}, {65, 128}, {129, 256}} {
		batch(span[0], span[1])
		samples = append(samples, auditTakeSample(fmt.Sprintf("new_%d", span[1])))
	}
	for round := 1; round <= 2; round++ {
		batch(1, 256)
		samples = append(samples, auditTakeSample(fmt.Sprintf("repeat_%d", round)))
	}
	debug.FreeOSMemory()
	samples = append(samples, auditTakeSample("after_FreeOSMemory"))
	for _, s := range samples {
		t.Logf("DIAGNOSTIC %s plans=%d HeapAlloc=%d HeapObjects=%d TotalAlloc=%d HeapIdle=%d HeapReleased=%d StackInuse=%d", s.Phase, s.Plans, s.HeapAlloc, s.HeapObjects, s.TotalAlloc, s.HeapIdle, s.HeapReleased, s.StackInuse)
		if scenario != "dynamic_types" && s.Plans != samples[0].Plans {
			t.Errorf("fixed types changed plan count: %d -> %d", samples[0].Plans, s.Plans)
		}
	}
	if directory := os.Getenv("DEEPCOPY_AUDIT_OUTPUT"); directory != "" {
		data, err := json.MarshalIndent(samples, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(directory, scenario+".json"), append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(directory, scenario+".heap"))
		if err != nil {
			t.Fatal(err)
		}
		err = pprof.WriteHeapProfile(f)
		closeErr := f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}

type auditSample struct {
	Phase                                                                  string
	Plans                                                                  int
	HeapAlloc, HeapObjects, TotalAlloc, HeapIdle, HeapReleased, StackInuse uint64
}

func auditTakeSample(phase string) auditSample {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	s := auditSample{Phase: phase, HeapAlloc: m.HeapAlloc, HeapObjects: m.HeapObjects, TotalAlloc: m.TotalAlloc, HeapIdle: m.HeapIdle, HeapReleased: m.HeapReleased, StackInuse: m.StackInuse}
	copyPlans.Range(func(_, _ any) bool { s.Plans++; return true })
	return s
}

type auditPayload [128]byte

// Four runs, fixed backing type. Inputs vary only exposed boundaries and order.
type auditBacking struct {
	P [64]*auditPayload
	N [16]uintptr
	Q [16]*auditPayload
	M [64]uintptr
}
type auditPN struct {
	P *auditPayload
	N uintptr
}
type auditNP struct {
	N uintptr
	P *auditPayload
}
type auditUnion struct {
	P, Q   []*[2]*auditPayload
	N, M   []*[2]uintptr
	PN, QM *auditPN
	NQ     *auditNP
}

//go:noinline
func auditMakeUnion(seed int) (*auditUnion, *auditBacking) {
	b := new(auditBacking)
	payload := &auditPayload{23}
	for i := range 64 {
		b.P[i] = payload
		b.M[i] = uintptr(i + 65)
	}
	for i := range b.Q {
		b.Q[i] = payload
		b.N[i] = uintptr(i + 1)
	}
	a, z := 18+(seed-1)%16, 18+(seed-1)/16
	g := &auditUnion{}
	for i := 64 - a; i < 63; i++ {
		g.P = append(g.P, (*[2]*auditPayload)(b.P[i:]))
	}
	for i := 0; i < 15; i++ {
		g.N = append(g.N, (*[2]uintptr)(b.N[i:]))
		g.Q = append(g.Q, (*[2]*auditPayload)(b.Q[i:]))
	}
	for i := 0; i < z-1; i++ {
		g.M = append(g.M, (*[2]uintptr)(b.M[i:]))
	}
	// Each two-word view is within one allocation and exactly matches its GC layout.
	g.PN = (*auditPN)(unsafe.Add(unsafe.Pointer(b), unsafe.Offsetof(b.N)-unsafe.Sizeof(uintptr(0))))
	g.NQ = (*auditNP)(unsafe.Add(unsafe.Pointer(b), unsafe.Offsetof(b.Q)-unsafe.Sizeof(uintptr(0))))
	g.QM = (*auditPN)(unsafe.Add(unsafe.Pointer(b), unsafe.Offsetof(b.M)-unsafe.Sizeof(uintptr(0))))
	if seed%2 == 0 {
		slices.Reverse(g.P)
		slices.Reverse(g.N)
		slices.Reverse(g.Q)
		slices.Reverse(g.M)
	}
	return g, b
}

func auditCheckUnion(t *testing.T, src, got *auditUnion) {
	t.Helper()
	for i, group := range [][]*[2]*auditPayload{got.P, got.Q} {
		original := [][]*[2]*auditPayload{src.P, src.Q}[i]
		for j, v := range group {
			if v == original[j] || v[0] == original[j][0] || v[0][0] != 23 || v[0] != v[1] {
				t.Fatal("pointer values/isolation changed")
			}
		}
	}
	for i, group := range [][]*[2]uintptr{got.N, got.M} {
		for j, v := range group {
			original := [][]*[2]uintptr{src.N, src.M}[i][j]
			if v == original || *v != *original {
				t.Fatal("scalar values/isolation changed")
			}
		}
	}
	// All views must retain their relative offsets in a connected union, in either discovery order.
	sourceBase, targetBase := unsafe.Pointer(src.PN), unsafe.Pointer(got.PN)
	check := func(a, b unsafe.Pointer) {
		sourceDelta := int64(uintptr(a)) - int64(uintptr(sourceBase))
		targetDelta := int64(uintptr(b)) - int64(uintptr(targetBase))
		if sourceDelta != targetDelta {
			t.Fatalf("relative offset changed: %d != %d", sourceDelta, targetDelta)
		}
		if uintptr(b)%unsafe.Alignof(uintptr(0)) != 0 {
			t.Fatal("misaligned view")
		}
	}
	check(unsafe.Pointer(src.NQ), unsafe.Pointer(got.NQ))
	check(unsafe.Pointer(src.QM), unsafe.Pointer(got.QM))
	for i := range src.P {
		check(unsafe.Pointer(src.P[i]), unsafe.Pointer(got.P[i]))
	}
	for i := range src.Q {
		check(unsafe.Pointer(src.Q[i]), unsafe.Pointer(got.Q[i]))
	}
	for i := range src.N {
		check(unsafe.Pointer(src.N[i]), unsafe.Pointer(got.N[i]))
	}
	for i := range src.M {
		check(unsafe.Pointer(src.M[i]), unsafe.Pointer(got.M[i]))
	}
}

//go:noinline
func auditExerciseUnion(t *testing.T, seed int, copyIt bool) {
	src, b := auditMakeUnion(seed)
	if copyIt {
		got := DeepCopyStruct(src).(*auditUnion)
		auditCheckUnion(t, src, got)
		runtime.KeepAlive(got)
	}
	runtime.KeepAlive(b)
	runtime.KeepAlive(src)
}

type auditAligned struct {
	Bytes []byte
	Zero  *[0]uint64
}

//go:noinline
func auditExerciseAlignment(t *testing.T, seed int) {
	b := new([8192]uint64)
	raw := unsafe.Slice((*byte)(unsafe.Pointer(b)), int(unsafe.Sizeof(*b)))
	start := seed % 8
	src := &auditAligned{Bytes: raw[start : start+seed : start+seed*9], Zero: (*[0]uint64)(unsafe.Pointer(&b[1]))}
	src.Bytes[0] = 19
	got := DeepCopyStruct(src).(*auditAligned)
	if len(got.Bytes) != seed || cap(got.Bytes) != seed*9 || got.Bytes[0] != 19 {
		t.Fatal("aligned slice shape changed")
	}
	if unsafe.Pointer(got.Zero) != unsafe.Pointer(&got.Bytes[:cap(got.Bytes)][8-start]) || uintptr(unsafe.Pointer(got.Zero))%8 != 0 {
		t.Fatal("zero-sized interior alias/alignment changed")
	}
	for _, v := range got.Bytes[len(got.Bytes):cap(got.Bytes)] {
		if v != 0 {
			t.Fatal("nonzero spare capacity")
		}
	}
	runtime.KeepAlive(b)
	runtime.KeepAlive(got)
}

type auditInterface struct{ Value any }

// This fixture intentionally creates types. Its paired no-copy control separates
// caller-created reflect metadata from additional copyPlans entries.
//
//go:noinline
func auditExerciseDynamic(t *testing.T, seed int, copyIt bool) {
	typ := reflect.StructOf([]reflect.StructField{{Name: "Bytes", Type: reflect.TypeFor[[]byte](), Tag: reflect.StructTag(fmt.Sprintf(`audit:"%d"`, seed))}})
	v := reflect.New(typ)
	v.Elem().Field(0).SetBytes([]byte{1, 2, 3})
	src := &auditInterface{Value: v.Interface()}
	if copyIt {
		got := DeepCopyStruct(src).(*auditInterface)
		out := reflect.ValueOf(got.Value)
		out.Elem().Field(0).Index(0).SetUint(9)
		if v.Elem().Field(0).Index(0).Uint() != 1 {
			t.Fatal("dynamic interface shares mutable bytes")
		}
		runtime.KeepAlive(got)
	}
	runtime.KeepAlive(src)
}

type auditNode struct {
	Next, Peer *auditNode
	ID         int
	Bytes      []byte
	Inside     *int
	Any        any
}
type auditGraph struct {
	Nodes []*auditNode
	Keys  map[*int]*auditNode
	Alias []byte
	Self  *auditGraph
}

//go:noinline
func auditExerciseGraph(t *testing.T, seed int) {
	g := &auditGraph{Nodes: make([]*auditNode, seed), Keys: make(map[*int]*auditNode)}
	g.Self = g
	for i := range seed {
		n := &auditNode{ID: i, Bytes: make([]byte, 1+i%13, 16+seed)}
		n.Inside = &n.ID
		g.Nodes[i] = n
		g.Keys[n.Inside] = n
	}
	for i, n := range g.Nodes {
		n.Next = g.Nodes[(i+1)%seed]
		n.Peer = g.Nodes[(i*7+seed/3)%seed]
		switch i % 3 {
		case 0:
			n.Any = n.Next
		case 1:
			n.Any = n.Bytes
		case 2:
			n.Any = i
		}
	}
	g.Alias = g.Nodes[0].Bytes
	if seed%2 == 0 {
		slices.Reverse(g.Nodes)
	}
	got := DeepCopyStruct(g).(*auditGraph)
	if got.Self != got || &got.Alias[0] == &g.Alias[0] {
		t.Fatal("root identity or isolation changed")
	}
	for i, n := range got.Nodes {
		original := g.Nodes[i]
		if n == original || n.Inside != &n.ID || got.Keys[n.Inside] != n || n.Next.ID != original.Next.ID || n.Peer.ID != original.Peer.ID || len(n.Bytes) != len(original.Bytes) || cap(n.Bytes) != cap(original.Bytes) {
			t.Fatal("graph shape changed")
		}
	}
	runtime.KeepAlive(got)
}

//go:noinline
func auditPrepareLifetime(t *testing.T) (*auditPN, weak.Pointer[auditBacking], weak.Pointer[auditPayload], weak.Pointer[auditUnion]) {
	src, b := auditMakeUnion(179)
	got := DeepCopyStruct(src).(*auditUnion)
	auditCheckUnion(t, src, got)
	return got.QM, weak.Make(b), weak.Make(src.PN.P), weak.Make(got)
}

//go:noinline
func auditStoreLater(p *auditPN) weak.Pointer[auditPayload] {
	v := &auditPayload{77}
	p.P = v
	return weak.Make(v)
}

//go:noinline
func auditStoreInteger(p *uintptr) weak.Pointer[auditPayload] {
	v := &auditPayload{88}
	*p = uintptr(unsafe.Pointer(v))
	return weak.Make(v)
}
func auditGC() {
	for range 5 {
		runtime.GC()
		runtime.Gosched()
	}
}

//go:noinline
func auditLiveInterior(t *testing.T) (weak.Pointer[auditPN], weak.Pointer[auditPayload]) {
	interior, source, payload, root := auditPrepareLifetime(t)
	auditGC()
	if source.Value() != nil || payload.Value() != nil || root.Value() != nil {
		t.Fatal("source allocation/payload or dead copied root retained")
	}
	if interior.P == nil || interior.P[0] != 23 || interior.N != 65 {
		t.Fatal("interior pointer failed to keep copied union alive")
	}
	written := auditStoreLater(interior)
	integer := auditStoreInteger(&interior.N)
	auditGC()
	if written.Value() == nil || interior.P[0] != 77 {
		t.Fatal("late pointer write not scanned")
	}
	if integer.Value() != nil {
		t.Fatal("uintptr incorrectly retained an object")
	}
	probe := weak.Make(interior)
	runtime.KeepAlive(interior)
	return probe, written
}
func TestDeepCopyAuditInteriorLifetime(t *testing.T) {
	union, payload := auditLiveInterior(t)
	auditGC()
	if union.Value() != nil || payload.Value() != nil {
		t.Fatal("copied union or late-written payload retained after dropping interior pointer")
	}
}

// The element has size 4 but alignment 1; the slice starts at residue 1 mod 8.
// No whole number of elements can reproduce that residue in aligned blocks.
type auditFallbackAligned struct {
	Bytes [][4]byte
	Zero  *[0]uint64
}

//go:noinline
func auditExerciseAlignmentFallback(t *testing.T, seed int) {
	b := new([8192]uint64)
	raw := unsafe.Slice((*[4]byte)(unsafe.Add(unsafe.Pointer(b), 1)), seed*9)
	src := &auditFallbackAligned{Bytes: raw[:seed], Zero: (*[0]uint64)(unsafe.Pointer(&b[1]))}
	src.Bytes[0][0] = 19
	got := DeepCopyStruct(src).(*auditFallbackAligned)
	if len(got.Bytes) != seed || cap(got.Bytes) != seed*9 || got.Bytes[0][0] != 19 {
		t.Fatal("fallback slice shape changed")
	}
	if unsafe.Pointer(got.Zero) != unsafe.Add(unsafe.Pointer(unsafe.SliceData(got.Bytes)), 7) || uintptr(unsafe.Pointer(got.Zero))%8 != 0 {
		t.Fatal("fallback lost interior alignment")
	}
	for _, v := range got.Bytes[len(got.Bytes):cap(got.Bytes)] {
		if v != [4]byte{} {
			t.Fatal("fallback copied spare capacity")
		}
	}
	runtime.KeepAlive(b)
	runtime.KeepAlive(got)
}
