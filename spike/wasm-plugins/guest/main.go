// Гостевой плагин «hello» для PoC серверных wasm-плагинов OC. Собирается как reactor:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o hello.wasm .
//
// ABI (всё остальное — JSON поверх него, сигнатуры не меняются):
//
//	экспорты плагина: alloc(n) -> ptr, dealloc(ptr), oc_init(ptr,len) -> i64, oc_call(mp,ml,ap,al) -> i64
//	импорт хоста:     oc.host_call(op_ptr,op_len,arg_ptr,arg_len) -> i64
//
// i64 = (ptr << 32) | len — буфер в памяти плагина; 0 — пусто. Буфер, который вернул плагин хосту, хост читает и освобождает
// через dealloc; буфер, который вернул хост плагину (через alloc плагина), плагин освобождает сам.
package main

import (
	"encoding/json"
	"strconv"
	"unsafe"
)

// ---- ABI ----

var live = map[uint32][]byte{} // удерживаем буферы от GC, пока ими пользуется хост (GC в Go не двигает память)

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

func unpack(v uint64) []byte {
	if v == 0 {
		return nil
	}
	p, n := uint32(v>>32), uint32(v)
	out := make([]byte, n)
	copy(out, unsafe.Slice((*byte)(unsafe.Pointer(uintptr(p))), n))
	dealloc(p)
	return out
}

func read(p, n uint32) []byte {
	if n == 0 {
		return nil
	}
	out := make([]byte, n)
	copy(out, unsafe.Slice((*byte)(unsafe.Pointer(uintptr(p))), n))
	return out
}

// host вызывает операцию хоста: op и JSON-аргумент, ответ — JSON.
func host(op string, arg any) []byte {
	a, _ := json.Marshal(arg)
	ob := []byte(op)
	var ap, al uint32
	if len(a) > 0 {
		ap, al = alloc(uint32(len(a))), uint32(len(a))
		copy(live[ap], a)
	}
	op0 := alloc(uint32(len(ob)))
	copy(live[op0], ob)
	res := unpack(hostCall(op0, uint32(len(ob)), ap, al))
	dealloc(op0)
	if ap != 0 {
		dealloc(ap)
	}
	return res
}

func log(level, msg string, fields map[string]any) {
	host("log", map[string]any{"level": level, "msg": msg, "fields": fields})
}

// ---- плагин ----

type route struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Handler string `json:"handler"`
}

type meta struct {
	ID         string  `json:"id"`
	Version    string  `json:"version"`
	APIVersion int     `json:"apiVersion"`
	Routes     []route `json:"routes"`
}

type request struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query"`
}

type response struct {
	Status int            `json:"status"`
	Body   map[string]any `json:"body"`
}

//go:wasmexport oc_init
func ocInit(ptr, n uint32) uint64 {
	_ = read(ptr, n) // {"apiVersion":1,"config":{...}} — в PoC не используем
	log("info", "hello plugin initialised", nil)
	m, _ := json.Marshal(meta{
		ID: "hello", Version: "0.1.0", APIVersion: 1,
		Routes: []route{{"GET", "/greet", "greet"}, {"GET", "/boom", "boom"}},
	})
	return pack(m)
}

//go:wasmexport oc_call
func ocCall(mp, ml, ap, al uint32) uint64 {
	name := string(read(mp, ml))
	arg := read(ap, al)
	switch name {
	case "greet":
		var req request
		_ = json.Unmarshal(arg, &req)
		who := req.Query["name"]
		if who == "" {
			who = "world"
		}
		// состояние живёт в хосте (storage), а не в памяти плагина: так экземпляров может быть несколько
		var cur struct {
			Value string `json:"value"`
		}
		_ = json.Unmarshal(host("storage.get", map[string]string{"key": "greeted"}), &cur)
		n, _ := strconv.Atoi(cur.Value)
		n++
		host("storage.set", map[string]string{"key": "greeted", "value": strconv.Itoa(n)})
		log("info", "greet", map[string]any{"name": who, "count": n})
		out, _ := json.Marshal(response{200, map[string]any{"hello": who, "count": n}})
		return pack(out)
	case "boom":
		log("warn", "about to panic", nil)
		var m map[string]int
		m["x"] = 1 // паника → trap; хост должен её пережить и перезапустить плагин
		return 0
	}
	out, _ := json.Marshal(response{404, map[string]any{"error": "unknown handler " + name}})
	return pack(out)
}

func main() {}
