package server

import (
	"github.com/compico/overlaycomposer/internal/market"
	"github.com/compico/overlaycomposer/internal/wasmplugins"
)

// wasmHost — менеджер wasm-плагинов глазами магазина: куда ставить, как проверить файл и что уже загружено.
type wasmHost struct{ m *wasmplugins.Manager }

func (h wasmHost) Dir() string {
	if d := h.m.Dirs(); len(d) > 0 {
		return d[0]
	}
	return ""
}

func (h wasmHost) Inspect(data []byte) (id, version string, perms []string, err error) {
	meta, err := h.m.Inspect(data)
	if err != nil {
		return "", "", nil, err
	}
	return meta.ID, meta.Version, meta.Permissions, nil
}

func (h wasmHost) Plugins() []market.WasmPlugin {
	infos, _ := h.m.List()
	out := make([]market.WasmPlugin, 0, len(infos))
	for _, in := range infos {
		out = append(out, market.WasmPlugin{ID: in.Meta.ID, Version: in.Meta.Version, SHA256: in.SHA256, File: in.File, Permissions: in.Meta.Permissions})
	}
	return out
}

func (h wasmHost) Rescan() { h.m.Scan() }
