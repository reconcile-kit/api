package resource

import (
	"cmp"
	"reflect"
	"slices"
	"sync"
	"time"
	"unsafe"
)

// DeepCopyStruct returns a deep copy of a struct or a non-nil pointer to a struct,
// preserving the input's type. Invalid inputs panic. The source must not change
// during copying.
//
// A struct input follows Go value semantics: pointers to the caller's root or
// its inline fields are copied as separate reachable objects, not rebound to the
// returned value. Pass a pointer when the root's identity must be preserved.
//
// Exported and unexported fields are copied, including pointer, map and slice
// cycles. Each call owns its copies; only immutable type metadata is cached.
// Slices retain len and cap, but only elements below len are copied. Identical
// slice views share their copied storage; distinct overlapping views need not.
// Views with spare capacity may be detached from an array owner to keep their
// unused tail zero without changing the owner's other elements.
//
// time.Time is copied by value, preserving its location and monotonic clock data.
// Functions, channels and unsafe.Pointer values retain their original identity.
// Unsafe views that disagree about whether a reference slot is deep-copied use
// separate storage. Interior pointers bind to a compatible copied view; sharing
// between these conflicting views is not preserved.
// Objects that prohibit copying, such as a used sync.Mutex, are not supported.
func DeepCopyStruct(src interface{}) interface{} {
	value := reflect.ValueOf(src)
	structValue := value.Kind() == reflect.Struct
	if !structValue && (value.Kind() != reflect.Pointer || value.IsNil() || value.Type().Elem().Kind() != reflect.Struct) {
		panic("DeepCopyStruct: input must be a struct or a non-nil pointer to a struct")
	}

	state := copyState{references: make(map[copyReference]*copyTarget)}
	state.prepare(value)
	state.allocate()
	if structValue {
		return state.copyValue(value).Interface()
	}

	target := state.references[referenceOf(value)]
	target.copied = true
	state.copyInto(target.value.Elem(), value.Elem())
	if target.value.Type() != value.Type() {
		return target.value.Convert(value.Type()).Interface()
	}
	return target.value.Interface()
}

// Plans never retain reflect.Values, source addresses or destination objects.
// Build a complete plan before publishing it so concurrent first use is safe.
var copyPlans sync.Map // map[reflect.Type]*copyPlan

var copyTimeType = reflect.TypeFor[time.Time]()

type copyPlan struct {
	direct    bool
	slotModes copySlotMode
	fields    []copyField
}

type copyField struct {
	index   int
	private bool
}

func planFor(typ reflect.Type) *copyPlan {
	if cached, ok := copyPlans.Load(typ); ok {
		return cached.(*copyPlan)
	}

	plan := &copyPlan{direct: copiesByValue(typ), slotModes: copySlotModesFor(typ)}
	if !plan.direct && typ.Kind() == reflect.Struct {
		for i := range typ.NumField() {
			field := typ.Field(i)
			if copiesByValue(field.Type) {
				continue
			}
			plan.fields = append(plan.fields, copyField{index: i, private: !field.IsExported()})
		}
	}
	cached, _ := copyPlans.LoadOrStore(typ, plan)
	return cached.(*copyPlan)
}

func copiesByValue(typ reflect.Type) bool {
	if typ == copyTimeType {
		return true
	}
	switch typ.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		return false
	case reflect.Array:
		return copiesByValue(typ.Elem())
	case reflect.Struct:
		for i := range typ.NumField() {
			if !copiesByValue(typ.Field(i).Type) {
				return false
			}
		}
	}
	return true
}

// Keep actual pointers, not uintptrs, so references keep their objects alive.
// Slice length and capacity distinguish different views of the same array.
type copyReference struct {
	typ      reflect.Type
	pointer  unsafe.Pointer
	length   int
	capacity int
}

type copyTarget struct {
	value  reflect.Value
	copied bool
}

type copyAllocation struct {
	reference copyReference
	size      uintptr
	storage   *copyStorage
}

// Storage is selected before allocation so all aliases can contribute their
// alignment requirements. The source and destination remain GC-tracked pointers.
type copyStorage struct {
	reference    copyReference
	alignment    reflect.Type
	pointer      unsafe.Pointer
	pointerUnion bool
	union        *copyPointerUnion
}

type copyRegion struct {
	storage *copyStorage
	end     uintptr
}

func (ref copyReference) storageSize() uintptr {
	size := ref.typ.Elem().Size()
	if ref.typ.Kind() == reflect.Slice {
		return uintptr(ref.capacity) * size
	}
	return size
}

type copyState struct {
	references  map[copyReference]*copyTarget
	allocations []copyAllocation
}

func referenceOf(src reflect.Value) copyReference {
	ref := copyReference{pointer: src.UnsafePointer()}
	switch src.Kind() {
	case reflect.Pointer:
		ref.typ = reflect.PointerTo(src.Type().Elem())
	case reflect.Map:
		ref.typ = reflect.MapOf(src.Type().Key(), src.Type().Elem())
	case reflect.Slice:
		ref.typ = reflect.SliceOf(src.Type().Elem())
		ref.length = src.Len()
		ref.capacity = src.Cap()
	}
	return ref
}

// Discover the reference graph before copying any data. This also lets a pointer
// to a struct field use the owner's destination even if the pointer was visited
// before its owner. No allocation or table entry is needed for ordinary fields.
func (state *copyState) prepare(src reflect.Value) {
	plan := planFor(src.Type())
	if plan.direct {
		return
	}

	switch src.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if src.IsNil() {
			return
		}
		ref := referenceOf(src)
		if _, exists := state.references[ref]; exists {
			return
		}
		target := &copyTarget{}
		state.references[ref] = target
		switch src.Kind() {
		case reflect.Pointer:
			state.allocations = append(state.allocations, copyAllocation{
				reference: ref, size: src.Type().Elem().Size(),
			})
			state.prepare(src.Elem())
		case reflect.Map:
			target.value = reflect.MakeMapWithSize(ref.typ, src.Len())
			iter := src.MapRange()
			for iter.Next() {
				state.prepare(iter.Key())
				state.prepare(iter.Value())
			}
		case reflect.Slice:
			state.allocations = append(state.allocations, copyAllocation{
				reference: ref, size: uintptr(src.Len()) * src.Type().Elem().Size(),
			})
			state.prepareElements(src)
		}
	case reflect.Interface:
		if !src.IsNil() {
			state.prepare(src.Elem())
		}
	case reflect.Struct:
		src = addressableCopySource(src)
		for _, field := range plan.fields {
			state.prepare(copyFieldValue(src, field))
		}
	case reflect.Array:
		state.prepareElements(src)
	}
}

func (state *copyState) prepareElements(src reflect.Value) {
	if planFor(src.Type().Elem()).direct {
		return
	}
	for i := range src.Len() {
		state.prepare(src.Index(i))
	}
}

func (state *copyState) allocate() {
	state.planStorage()
	for _, allocation := range state.allocations {
		storage := allocation.storage
		if storage.pointer == nil {
			storage.pointer = storage.allocate()
		}
		ref := allocation.reference
		offset := uintptr(ref.pointer) - uintptr(storage.reference.pointer)
		state.references[ref].value = copyValueAt(ref, unsafe.Add(storage.pointer, offset))
	}
}

// Sort source storage by address, putting a containing allocation before its
// subobjects. Only visible slice elements own nonzero values; zero-sized aliases
// may also share spare capacity without populating the unused tail.
func (state *copyState) planStorage() {
	sortCopyAllocations(state.allocations)
	groups := partitionCopyAllocations(state.allocations)
	if len(groups) == 0 {
		state.planCompatibleStorage()
		return
	}
	state.allocations = nil
	for _, allocations := range groups {
		group := copyState{allocations: allocations}
		group.planCompatibleStorage()
		state.allocations = append(state.allocations, group.allocations...)
	}
}

// Each group has mutually compatible reference slots. Array/struct unions and
// containing owners must stay within that group so they cannot rejoin views
// that were separated because of conflicting copy semantics.
func (state *copyState) planCompatibleStorage() {
	if state.mergeArrayStorage() {
		// A union may now contain another allocation at the same address.
		sortCopyAllocations(state.allocations)
	}
	if state.mergePointerStorage() {
		sortCopyAllocations(state.allocations)
	}

	var owner, pointerOwner, zeroOwner copyRegion
	var address unsafe.Pointer
	var shared *copyStorage
	for i := range state.allocations {
		allocation := &state.allocations[i]
		ref := allocation.reference
		if ref.pointer != address {
			address = ref.pointer
			shared = nil
		}
		storage := storageForCopy(*allocation, pointerOwner, owner, zeroOwner, shared)
		allocation.storage = storage
		if ref.typ.Elem().Align() > storage.alignment.Align() {
			storage.alignment = ref.typ.Elem()
		}
		if shared == nil {
			shared = storage
		}
		end := uintptr(ref.pointer) + allocation.size
		if ref.typ.Kind() == reflect.Pointer && allocation.size > 0 && end > pointerOwner.end {
			// A crossing slice may need separate storage, but it must not
			// displace an array/struct owner for subsequent interior pointers.
			pointerOwner = copyRegion{storage: storage, end: end}
		}
		if allocation.size > 0 && end > owner.end {
			owner = copyRegion{storage: storage, end: end}
		}
		physicalSize := max(allocation.size, ref.storageSize())
		end = uintptr(ref.pointer) + physicalSize
		if physicalSize > 0 && end > zeroOwner.end {
			zeroOwner = copyRegion{storage: storage, end: end}
		}
	}
}

func sortCopyAllocations(allocations []copyAllocation) {
	slices.SortFunc(allocations, func(a, b copyAllocation) int {
		if order := cmp.Compare(uintptr(a.reference.pointer), uintptr(b.reference.pointer)); order != 0 {
			return order
		}
		if order := cmp.Compare(b.size, a.size); order != 0 {
			return order
		}
		if a.size == 0 {
			// Empty slices with capacity provide storage for zero-sized aliases
			// at this address, even though they have no visible elements.
			if order := cmp.Compare(b.reference.storageSize(), a.reference.storageSize()); order != 0 {
				return order
			}
		}
		if a.reference.typ.Kind() == reflect.Slice && b.reference.typ.Kind() == reflect.Pointer {
			// A slice can own a visible array of the same size while retaining
			// its own zeroed spare capacity.
			return -1
		}
		if a.reference.typ.Kind() == reflect.Pointer && b.reference.typ.Kind() == reflect.Slice {
			return 1
		}
		return 0
	})
}

// Partially overlapping array pointers can be formed from slices without unsafe.
// Merge their complete intervals before assigning any interior pointers. Flatten
// nested arrays to a common element type and allocate a typed slice, keeping the
// GC layout without caching an array type for every union length.
func (state *copyState) mergeArrayStorage() bool {
	arrays := make(map[reflect.Type]*copyAllocation)
	merged := false
	for i := range state.allocations {
		allocation := &state.allocations[i]
		ref := allocation.reference
		if ref.typ.Kind() != reflect.Pointer || ref.typ.Elem().Kind() != reflect.Array || allocation.size == 0 {
			continue
		}
		elem := ref.typ.Elem()
		for elem.Kind() == reflect.Array {
			elem = elem.Elem()
		}
		start := uintptr(ref.pointer)
		previous := overlappingCopyArray(arrays, elem, start)
		if previous == nil {
			arrays[elem] = allocation
			continue
		}
		arrays[elem] = previous
		end := start + allocation.size
		base := uintptr(previous.reference.pointer)
		if end <= base+previous.size {
			continue
		}
		previous.size = end - base
		count := int(previous.size / elem.Size())
		if previous.storage == nil {
			previous.storage = &copyStorage{
				reference: copyReference{typ: reflect.SliceOf(elem), pointer: previous.reference.pointer},
				alignment: previous.reference.typ.Elem(), pointerUnion: true,
			}
		}
		previous.storage.reference.length = count
		previous.storage.reference.capacity = count
		merged = true
	}
	return merged
}

func overlappingCopyArray(arrays map[reflect.Type]*copyAllocation, elem reflect.Type, start uintptr) *copyAllocation {
	if previous := arrays[elem]; previous != nil && start < uintptr(previous.reference.pointer)+previous.size {
		return previous
	}
	for other, previous := range arrays {
		if start >= uintptr(previous.reference.pointer)+previous.size {
			continue
		}
		// Go pointer conversions can ignore struct tags. Such aliases have
		// the same size, alignment and GC layout despite different types.
		if reflect.PointerTo(elem).ConvertibleTo(reflect.PointerTo(other)) {
			return previous
		}
	}
	return nil
}

func storageForCopy(
	allocation copyAllocation,
	pointerOwner, owner, zeroOwner copyRegion,
	shared *copyStorage,
) *copyStorage {
	ref := allocation.reference
	start := uintptr(ref.pointer)
	if pointerOwner.storage != nil && start < pointerOwner.end && start+allocation.size <= pointerOwner.end && canShareCopyStorage(ref, pointerOwner.storage) {
		return pointerOwner.storage
	}
	if ref.storageSize() == 0 {
		if shared != nil {
			return shared
		}
		// Zero-sized objects write no values, so they can use the entire
		// allocation, including a slice's zeroed spare capacity.
		if zeroOwner.storage != nil && start < zeroOwner.end {
			return zeroOwner.storage
		}
	}
	if owner.storage != nil && start < owner.end && start+allocation.size <= owner.end && canShareCopyStorage(ref, owner.storage) {
		return owner.storage
	}
	if allocation.storage != nil {
		return allocation.storage
	}
	return &copyStorage{reference: ref, alignment: ref.typ.Elem()}
}

func canShareCopyStorage(ref copyReference, owner *copyStorage) bool {
	if ref.typ.Kind() == reflect.Pointer {
		return true
	}
	if ref.typ.Elem().Size() == 0 {
		// Zero-sized elements cannot contain live data in the unused tail.
		return true
	}
	// A containing object may have live values beyond this view's length.
	// Detach the view rather than clear those values or copy its unused tail.
	if ref.length != ref.capacity {
		return false
	}
	// Keep distinct views of the same slice independent. Slices of another
	// element type can refer to arrays nested inside the owner's elements.
	return owner.pointerUnion || owner.reference.typ.Kind() == reflect.Pointer || ref.typ.Elem() != owner.reference.typ.Elem()
}

func copyValueAt(ref copyReference, pointer unsafe.Pointer) reflect.Value {
	if ref.typ.Kind() == reflect.Pointer {
		return reflect.NewAt(ref.typ.Elem(), pointer)
	}
	value := reflect.SliceAt(ref.typ.Elem(), pointer, ref.capacity)
	if ref.length != ref.capacity {
		return value.Slice(0, ref.length)
	}
	return value
}

func (storage *copyStorage) allocate() unsafe.Pointer {
	if storage.union != nil {
		return storage.allocatePointerUnion()
	}
	ref := storage.reference
	elem := ref.typ.Elem()
	if ref.storageSize() != 0 && elem.Align() == storage.alignment.Align() {
		if ref.typ.Kind() == reflect.Pointer {
			return reflect.New(elem).UnsafePointer()
		}
		return reflect.MakeSlice(ref.typ, ref.length, ref.capacity).UnsafePointer()
	}

	if ref.typ.Kind() == reflect.Slice && ref.storageSize() != 0 {
		return storage.allocateAlignedSlice()
	}

	// Use typed storage so the GC retains any pointers in the value. The
	// alignment marker also covers zero-sized aliases of a byte pointer/slice.
	typ := elem
	if ref.typ.Kind() == reflect.Slice && elem.Size() != 0 {
		// A zero-capacity slice only needs a distinct, aligned address.
		// Nonempty slices use repeated storage, never capacity-specific types.
		typ = reflect.ArrayOf(0, elem)
	}
	// Retain the source base's alignment residue for aliases at interior
	// offsets. No address is reconstructed from a uintptr.
	prefix := uintptr(ref.pointer) % uintptr(storage.alignment.Align())
	wrapper := reflect.StructOf([]reflect.StructField{
		{Name: "Alignment", Type: reflect.ArrayOf(0, storage.alignment)},
		{Name: "Prefix", Type: reflect.ArrayOf(int(prefix), reflect.TypeFor[byte]())},
		{Name: "Value", Type: typ},
		// A nonzero allocation keeps distinct zero-sized source addresses
		// from collapsing into runtime.zerobase and merging pointer keys.
		{Name: "Padding", Type: reflect.TypeFor[byte]()},
	})
	return reflect.New(wrapper).Elem().Field(2).Addr().UnsafePointer()
}

func (state *copyState) copyInto(dst, src reflect.Value) {
	plan := planFor(src.Type())
	if plan.direct {
		dst.Set(src)
		return
	}

	switch src.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if src.IsNil() {
			dst.SetZero()
			return
		}
		target := state.references[referenceOf(src)]
		dst.Set(target.value)
		if target.copied {
			return
		}
		target.copied = true
		switch src.Kind() {
		case reflect.Pointer:
			state.copyInto(target.value.Elem(), src.Elem())
		case reflect.Map:
			state.copyMap(target.value, src)
		case reflect.Slice:
			state.copyElements(target.value, src)
		}
	case reflect.Interface:
		if src.IsNil() {
			dst.SetZero()
			return
		}
		dst.Set(state.copyValue(src.Elem()))
	case reflect.Struct:
		src = addressableCopySource(src)
		dst.Set(src)
		for _, field := range plan.fields {
			state.copyInto(copyFieldValue(dst, field), copyFieldValue(src, field))
		}
	case reflect.Array:
		state.copyElements(dst, src)
	}
}

func (state *copyState) copyValue(src reflect.Value) reflect.Value {
	if planFor(src.Type()).direct {
		return src
	}
	dst := reflect.New(src.Type()).Elem()
	state.copyInto(dst, src)
	return dst
}

func (state *copyState) copyMap(dst, src reflect.Value) {
	iter := src.MapRange()
	for iter.Next() {
		dst.SetMapIndex(state.copyValue(iter.Key()), state.copyValue(iter.Value()))
	}
}

func (state *copyState) copyElements(dst, src reflect.Value) {
	if planFor(src.Type().Elem()).direct {
		reflect.Copy(dst, src)
		return
	}
	for i := range src.Len() {
		state.copyInto(dst.Index(i), src.Index(i))
	}
}

func addressableCopySource(src reflect.Value) reflect.Value {
	if src.CanAddr() {
		return src
	}
	// Map entries and interface-held struct values are not addressable. A
	// temporary value allows reading their private fields without changing src.
	value := reflect.New(src.Type()).Elem()
	value.Set(src)
	return value
}

func copyFieldValue(value reflect.Value, field copyField) reflect.Value {
	value = value.Field(field.index)
	if !field.private {
		return value
	}
	// Re-expose the existing, addressable field with its exact type. The pointer
	// remains a GC-tracked unsafe.Pointer; no uintptr is converted to a pointer.
	return reflect.NewAt(value.Type(), value.Addr().UnsafePointer()).Elem()
}
