// Тестовый плагин для интеграционного теста wazero (wazero_test.go): собирается как reactor
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o testguest.wasm .
//
// ABI описан в docs/WASM_PLUGINS.md.
package main

import (
	"encoding/json"
	"strconv"
	"unsafe"
)

var live = map[uint32][]byte{} // буферы, которыми пользуется хост, не должны собираться GC

//go:wasmexport alloc
func alloc(n uint32) uint32 {
	b := make([]byte, n+1)
	p := uint32(uintptr(unsafe.Pointer(&b[0])))
	live[p] = b
	return p
}

//go:wasmexport dealloc
func dealloc(p uint32) { delete(live, p) }

//go:wasmimport oc host_call
func hostCall(opPtr, opLen, argPtr, argLen uint32) uint64

func pack(v any) uint64 {
	b, _ := json.Marshal(v)
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

func host(op string, arg any) []byte {
	a, _ := json.Marshal(arg)
	ob := []byte(op)
	op0, ap := alloc(uint32(len(ob))), alloc(uint32(len(a)))
	copy(live[op0], ob)
	copy(live[ap], a)
	v := hostCall(op0, uint32(len(ob)), ap, uint32(len(a)))
	dealloc(op0)
	dealloc(ap)
	if v == 0 {
		return nil
	}
	p, n := uint32(v>>32), uint32(v)
	out := read(p, n)
	dealloc(p)
	return out
}

func getInt(key string) int {
	var cur struct {
		Value int `json:"value"`
	}
	_ = json.Unmarshal(host("storage.get", map[string]string{"key": key}), &cur)
	return cur.Value
}

//go:wasmexport oc_meta
func ocMeta() uint64 {
	return pack(map[string]any{
		"id": "testguest", "name": "Test guest", "version": "0.1.0", "apiVersion": 1,
		"permissions": []string{"storage", "timer"},
		"routes": []map[string]string{
			{"method": "GET", "path": "/greet", "handler": "greet"},
			{"method": "GET", "path": "/ticks", "handler": "ticks"},
			{"method": "GET", "path": "/boom", "handler": "boom"},
		},
	})
}

//go:wasmexport oc_init
func ocInit(ptr, n uint32) uint64 {
	host("timer.set", map[string]any{"id": "t", "ms": 50, "repeat": true})
	return 0
}

//go:wasmexport oc_call
func ocCall(mp, ml, ap, al uint32) uint64 {
	name := string(read(mp, ml))
	arg := read(ap, al)
	switch name {
	case "on_event":
		var ev struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(arg, &ev)
		if ev.Type == "timer" {
			host("storage.set", map[string]any{"key": "ticks", "value": getInt("ticks") + 1})
		}
		return 0
	case "greet":
		var req struct {
			Query map[string]string `json:"query"`
		}
		_ = json.Unmarshal(arg, &req)
		n := getInt("greeted") + 1
		host("storage.set", map[string]any{"key": "greeted", "value": n})
		return pack(map[string]any{"body": map[string]any{"hello": req.Query["name"], "count": n, "n": strconv.Itoa(n)}})
	case "ticks":
		return pack(map[string]any{"body": map[string]any{"ticks": getInt("ticks")}})
	case "boom":
		var m map[string]int
		m["x"] = 1 // паника → trap; хост должен пересоздать экземпляр
	}
	return 0
}

func main() {}
