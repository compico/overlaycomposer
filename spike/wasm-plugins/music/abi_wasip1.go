//go:build wasip1

package main

// ABI-обёртка (см. ../README.md): alloc/dealloc/oc_init/oc_call у плагина и oc.host_call у хоста.
// Сборка: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o music.wasm .

import (
	"encoding/json"
	"time"
	"unsafe"
)

var live = map[uint32][]byte{}

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

func pack(b []byte) uint64 {
	if len(b) == 0 {
		return 0
	}
	p := alloc(uint32(len(b)))
	copy(live[p], b)
	return uint64(p)<<32 | uint64(len(b))
}

func readMem(p, n uint32) []byte {
	if n == 0 {
		return nil
	}
	out := make([]byte, n)
	copy(out, unsafe.Slice((*byte)(unsafe.Pointer(uintptr(p))), n))
	return out
}

func takeResult(v uint64) []byte {
	if v == 0 {
		return nil
	}
	p, n := uint32(v>>32), uint32(v)
	out := readMem(p, n)
	dealloc(p)
	return out
}

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
	res := takeResult(hostCall(op0, uint32(len(ob)), ap, al))
	dealloc(op0)
	if ap != 0 {
		dealloc(ap)
	}
	return res
}

type abiHost struct{}

func (abiHost) Get(key string) string {
	var r struct {
		Value string `json:"value"`
	}
	_ = json.Unmarshal(host("storage.get", map[string]string{"key": key}), &r)
	return r.Value
}
func (abiHost) Set(key, value string) {
	host("storage.set", map[string]string{"key": key, "value": value})
}
func (abiHost) Log(level, msg string, kv map[string]any) {
	host("log", map[string]any{"level": level, "msg": msg, "fields": kv})
}
func (abiHost) UpdateRedemption(rewardID, id, status string) {
	host("twitch.redemption.update", map[string]string{"rewardId": rewardID, "id": id, "status": status})
}

var plugin = &Plugin{H: abiHost{}, Now: time.Now}

type route struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Handler string `json:"handler"`
}

//go:wasmexport oc_init
func ocInit(ptr, n uint32) uint64 {
	var in struct {
		APIVersion int    `json:"apiVersion"`
		Config     Config `json:"config"`
	}
	_ = json.Unmarshal(readMem(ptr, n), &in)
	plugin.Cfg = in.Config
	host("timer.set", map[string]any{"id": "tick", "ms": 30000, "repeat": true})
	m, _ := json.Marshal(map[string]any{
		"id": "music", "version": "0.1.0", "apiVersion": 1,
		"subscriptions": []string{"twitch.redemption", "timer"},
		"routes": []route{
			{"GET", "/next", "next"}, {"POST", "/started", "started"}, {"POST", "/failed", "failed"}, {"GET", "/queue", "queue"},
		},
	})
	return pack(m)
}

type event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

//go:wasmexport oc_call
func ocCall(mp, ml, ap, al uint32) uint64 {
	name := string(readMem(mp, ml))
	arg := readMem(ap, al)
	if name == "on_event" {
		var ev event
		if err := json.Unmarshal(arg, &ev); err != nil {
			return 0
		}
		switch ev.Type {
		case "twitch.redemption":
			var r Redemption
			if json.Unmarshal(ev.Data, &r) == nil {
				plugin.OnRedemption(r)
			}
		case "timer":
			var t struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(ev.Data, &t)
			plugin.OnTimer(t.ID)
		}
		return 0
	}
	var req Request
	_ = json.Unmarshal(arg, &req)
	out, _ := json.Marshal(plugin.Handle(name, req))
	return pack(out)
}

func main() {}
