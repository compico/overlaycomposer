package server

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/compico/overlaycomposer/internal/events"
	"github.com/compico/overlaycomposer/internal/wasmplugins"
)

func TestDeckPluginTileHighlightFollowsEvents(t *testing.T) {
	s, _ := deckEnv(t)
	info := musicInfo(wasmplugins.StateRunning)
	info.Meta.Events = []wasmplugins.EventDecl{{ID: "np"}}
	info.Meta.Actions[0].ActiveWhen = &wasmplugins.ActiveWhen{Topic: "np", Field: "playing", Equals: json.RawMessage(`true`)}
	f := &fakeDeckPlugins{infos: []wasmplugins.Info{info}}
	bus := events.New(events.Options{})
	if err := bus.Declare("music", []string{"np"}); err != nil {
		t.Fatal(err)
	}
	s.deckPluginsOverride, s.deckEventsOverride = f, bus
	if code, b, _ := deckReq(t, s, "PUT", "/api/deck/layout", deckPluginLayout, true); code != 200 {
		t.Fatalf("save: %d %s", code, b)
	}
	on := func() []string { return s.deckStateNow().On }

	if got := on(); len(got) != 0 {
		t.Fatalf("событий не было: %v", got)
	}
	_ = bus.Publish("music", "np", json.RawMessage(`{"playing":true}`), true)
	if got := on(); !reflect.DeepEqual(got, []string{"toggle"}) {
		t.Fatalf("играет: %v", got)
	}
	_ = bus.Publish("music", "np", json.RawMessage(`{"playing":false}`), true)
	if got := on(); len(got) != 0 {
		t.Fatalf("пауза: %v", got)
	}
	_ = bus.Publish("music", "np", json.RawMessage(`{"playing":true}`), true)
	if len(on()) != 1 {
		t.Fatal("снова играет")
	}
	// плагин остановлен — плитка не подсвечена, подписка закрыта
	f.infos = []wasmplugins.Info{func() wasmplugins.Info { i := info; i.State = wasmplugins.StateStopped; return i }()}
	if got := on(); len(got) != 0 {
		t.Fatalf("остановлен: %v", got)
	}
	if len(s.deckWatch.m) != 0 {
		t.Fatalf("подписки не закрыты: %d", len(s.deckWatch.m))
	}
	// плагин вернулся — retained-значение подхватывается новой подпиской
	f.infos = []wasmplugins.Info{info}
	if len(on()) != 1 {
		t.Fatal("после возвращения плагина")
	}
	// выгрузка плагина закрывает канал: подписка создаётся заново без паники
	bus.Forget("music")
	_ = on()
	_ = bus.Declare("music", []string{"np"})
	_ = bus.Publish("music", "np", json.RawMessage(`{"playing":true}`), true)
	if len(on()) != 1 {
		t.Fatal("после перезагрузки плагина")
	}
	s.deckPluginsOverride, s.deckEventsOverride = nil, nil
}
