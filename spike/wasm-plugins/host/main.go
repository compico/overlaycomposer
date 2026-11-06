// PoC хоста серверных wasm-плагинов: wazero + ABI из ../README.md.
//
//	cd ../guest && GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o hello.wasm .
//	cd ../music && GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o music.wasm .
//	cd ../host && go run . -config '{"rewardId":"rw"}' ../music/music.wasm
//
// Эмуляция события Twitch (в OC его пришлёт EventSub) и работа аддона плеера:
//
//	curl -X POST 127.0.0.1:8099/dev/event -d '{"type":"twitch.redemption","data":{"id":"r1","rewardId":"rw","userLogin":"v","input":"https://music.yandex.ru/album/1/track/2"}}'
//	curl 127.0.0.1:8099/plugins/music/next
//	curl -X POST 127.0.0.1:8099/plugins/music/started -d '{"id":1}'
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
)

func main() {
	cfg := flag.String("config", "{}", "JSON-конфиг плагина (oc_init)")
	addr := flag.String("addr", "127.0.0.1:8099", "адрес HTTP")
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: host [-config JSON] <plugin.wasm>")
		os.Exit(2)
	}
	ctx := context.Background()
	wasm, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		panic(err)
	}
	p, err := load(ctx, wasm, json.RawMessage(*cfg))
	if err != nil {
		panic(err)
	}
	mux := p.routes()
	slog.Info("listening", "addr", *addr, "plugin", p.meta.ID)
	_ = http.ListenAndServe(*addr, mux)
}

// routes — маршруты плагина под /plugins/<id>/… и служебный /dev/event для проверки без Twitch.
func (p *plugin) routes() *http.ServeMux {
	mux := http.NewServeMux()
	for _, r := range p.meta.Routes {
		r := r
		mux.HandleFunc(r.Method+" /plugins/"+p.meta.ID+r.Path, func(w http.ResponseWriter, req *http.Request) {
			q := map[string]string{}
			for k, v := range req.URL.Query() {
				q[k] = v[0]
			}
			raw, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
			body := json.RawMessage(raw)
			if len(raw) > 0 && !json.Valid(raw) {
				body, _ = json.Marshal(string(raw))
			}
			resp, err := p.call(req.Context(), r.Handler, request{Method: req.Method, Path: req.URL.Path, Query: q, Body: body})
			if err != nil {
				http.Error(w, "plugin error", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.Status)
			_ = json.NewEncoder(w).Encode(resp.Body)
		})
	}
	mux.HandleFunc("POST /dev/event", func(w http.ResponseWriter, req *http.Request) {
		var ev struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(req.Body, 1<<20)).Decode(&ev); err != nil || ev.Type == "" {
			http.Error(w, "body must be {type, data}", http.StatusBadRequest)
			return
		}
		if err := p.deliver(req.Context(), ev.Type, ev.Data); err != nil {
			http.Error(w, "plugin error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	return mux
}
