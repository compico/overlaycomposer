//go:build wasip1

package ocplugin

import "unsafe"

// ABI хоста (см. docs/WASM_PLUGINS.md): плагин экспортирует alloc/dealloc/oc_meta/oc_init/oc_call, импортирует oc.host_call.
// i64 = ptr<<32 | len — буфер в памяти плагина; 0 — пусто.

var live = map[uint32][]byte{} // буферы, которыми пользуется хост, не должны собираться GC (Go не двигает память)

//go:wasmexport alloc
func alloc(n uint32) uint32 {
	b := make([]byte, n+1) // +1: нулевая длина тоже даёт валидный указатель
	p := uint32(uintptr(unsafe.Pointer(&b[0])))
	live[p] = b
	return p
}

//go:wasmexport dealloc
func dealloc(p uint32) { delete(live, p) }

//go:wasmimport oc host_call
func hostCall(opPtr, opLen, argPtr, argLen uint32) uint64

func pack(b []byte) uint64 {
	if len(b) == 0 {
		return 0
	}
	p := alloc(uint32(len(b)))
	copy(live[p], b)
	return uint64(p)<<32 | uint64(len(b))
}

func read(p, n uint32) []byte {
	if n == 0 {
		return nil
	}
	out := make([]byte, n)
	copy(out, unsafe.Slice((*byte)(unsafe.Pointer(uintptr(p))), n))
	return out
}

func abiHost(op string, arg []byte) []byte {
	ob := []byte(op)
	op0 := alloc(uint32(len(ob)))
	copy(live[op0], ob)
	ap := alloc(uint32(len(arg)))
	copy(live[ap], arg)
	v := hostCall(op0, uint32(len(ob)), ap, uint32(len(arg)))
	dealloc(op0)
	dealloc(ap)
	if v == 0 {
		return nil
	}
	p, n := uint32(v>>32), uint32(v)
	out := read(p, n) // буфер хост выделил через наш alloc — освобождаем сами
	dealloc(p)
	return out
}

func init() { host = abiHost }

func plugin() *Plugin {
	if current == nil {
		panic("ocplugin: call ocplugin.New in init()")
	}
	return current
}

//go:wasmexport oc_meta
func ocMeta() uint64 { return pack(plugin().metaJSON()) }

//go:wasmexport oc_init
func ocInit(ptr, n uint32) uint64 {
	plugin().init(read(ptr, n))
	return 0
}

//go:wasmexport oc_call
func ocCall(mp, ml, ap, al uint32) uint64 {
	return pack(plugin().dispatch(string(read(mp, ml)), read(ap, al)))
}
