package obsimport

import (
	"math"
	"testing"

	"github.com/compico/overlaycomposer/internal/model"
)

type fake map[string]map[string]any // "Type|name" → ответ

func (f fake) Request(typ string, d map[string]any) (map[string]any, bool) {
	key := typ
	for _, k := range []string{"sceneName", "inputName"} {
		if v, ok := d[k]; ok {
			key += "|" + v.(string)
		}
	}
	r, ok := f[key]
	return r, ok
}

func tf(px, py, w, h, sw, sh, rot float64, align int) map[string]any {
	return map[string]any{"positionX": px, "positionY": py, "width": w, "height": h, "sourceWidth": sw, "sourceHeight": sh,
		"rotation": rot, "alignment": float64(align), "scaleX": w / sw, "scaleY": h / sh}
}

func browser(name string, idx int, t map[string]any) map[string]any {
	return map[string]any{"sourceName": name, "sceneItemIndex": float64(idx), "sceneItemEnabled": true, "sourceType": "OBS_SOURCE_TYPE_INPUT",
		"inputKind": "browser_source", "isGroup": nil, "sceneItemTransform": t}
}

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.02 {
		t.Errorf("%s: got %v want %v", name, got, want)
	}
}

func TestImportBasic(t *testing.T) {
	f := fake{
		"GetVideoSettings": {"baseWidth": 2560.0, "baseHeight": 1440.0},
		"GetSceneItemList|Main": {"sceneItems": []any{
			browser("Chat", 1, tf(100, 100, 200, 100, 400, 200, 0, 5)),   // масштаб 0.5, левый верх
			browser("Alerts", 0, tf(960, 540, 800, 600, 800, 600, 0, 0)), // выравнивание по центру
			browser("Rot", 2, tf(100, 100, 200, 100, 200, 100, 90, 5)),   // 90° по часовой вокруг левого верха
			browser("Local", 3, tf(0, 0, 100, 100, 100, 100, 0, 5)),
			browser("Self", 4, tf(0, 0, 100, 100, 100, 100, 0, 5)),
			map[string]any{"sourceName": "Cam", "sceneItemIndex": 5.0, "sourceType": "OBS_SOURCE_TYPE_INPUT", "inputKind": "v4l2_input", "sceneItemTransform": map[string]any{}},
		}},
		"GetInputSettings|Chat":   {"inputSettings": map[string]any{"url": "https://chat.example.com/x?a=1", "width": 400.0, "height": 200.0}},
		"GetInputSettings|Alerts": {"inputSettings": map[string]any{"url": "https://alerts.example.com/"}},
		"GetInputSettings|Rot":    {"inputSettings": map[string]any{"url": "https://chat.example.com/x?a=1"}}, // тот же URL → тот же source
		"GetInputSettings|Local":  {"inputSettings": map[string]any{"url": "http://localhost:3000/w"}},
		"GetInputSettings|Self":   {"inputSettings": map[string]any{"url": "http://127.0.0.1:8000/overlay"}},
	}
	res, err := Import(f, "Main", []model.Source{{ID: "chat", Name: "Old", Type: "iframe"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Layout.Canvas.Width != 2560 {
		t.Errorf("canvas %+v", res.Layout.Canvas)
	}
	if len(res.Layout.Objects) != 4 {
		t.Fatalf("objects: %d %+v", len(res.Layout.Objects), res.Layout.Objects)
	}
	// порядок снизу вверх по sceneItemIndex: Alerts, Chat, Rot, Local
	o := res.Layout.Objects
	if o[0].Name != "Alerts" || o[1].Name != "Chat" || o[2].Name != "Rot" || o[3].Name != "Local" {
		t.Fatalf("order: %v %v %v %v", o[0].Name, o[1].Name, o[2].Name, o[3].Name)
	}
	near(t, "alerts.x", o[0].Transform.X, 960-400)
	near(t, "alerts.y", o[0].Transform.Y, 540-300)
	near(t, "chat.x", o[1].Transform.X, 100-100) // центр (200,150), размер источника 400×200 → x=0
	near(t, "chat.y", o[1].Transform.Y, 150-100)
	near(t, "chat.scale", o[1].Transform.ScaleX, 0.5)
	near(t, "chat.w", o[1].Transform.Width, 400)
	near(t, "rot.x", o[2].Transform.X, -50)
	near(t, "rot.y", o[2].Transform.Y, 150)
	near(t, "rot.rz", o[2].Transform.RotationZ, 90)
	if o[0].ZIndex >= o[1].ZIndex {
		t.Error("zIndex must grow")
	}

	// источники: chat уже существует с другим URL → создаётся новый; Chat и Rot делят один
	bySrc := map[string]model.Source{}
	for _, s := range res.Sources {
		bySrc[s.Name] = s
	}
	if !bySrc["Alerts"].Data.Proxy || !bySrc["Chat"].Data.Proxy {
		t.Error("внешние сайты должны идти через прокси")
	}
	if bySrc["Local"].Data.Proxy {
		t.Error("localhost без прокси")
	}
	if bySrc["Chat"].ID == "chat" {
		t.Error("id не должен совпадать с существующим")
	}
	if o[1].SourceID != o[2].SourceID {
		t.Error("тот же URL → тот же source")
	}
	if len(res.Skipped) != 2 { // Self и Cam
		t.Errorf("skipped: %+v", res.Skipped)
	}
}

func TestGroupTransform(t *testing.T) {
	f := fake{
		"GetSceneItemList|S": {"sceneItems": []any{
			map[string]any{"sourceName": "Grp", "sceneItemIndex": 0.0, "sceneItemEnabled": true, "sourceType": "OBS_SOURCE_TYPE_INPUT",
				"isGroup": true, "sceneItemTransform": tf(500, 0, 300, 300, 300, 300, 0, 5)},
		}},
		"GetGroupSceneItemList|Grp": {"sceneItems": []any{
			browser("W", 0, tf(10, 20, 100, 50, 100, 50, 0, 5)),
		}},
		"GetInputSettings|W": {"inputSettings": map[string]any{"url": "http://localhost/w"}},
	}
	res, err := Import(f, "S", nil)
	if err != nil {
		t.Fatal(err)
	}
	o := res.Layout.Objects[0]
	near(t, "x", o.Transform.X, 510)
	near(t, "y", o.Transform.Y, 20)
	if o.Name != "Grp / W" {
		t.Errorf("name %q", o.Name)
	}

	// группа масштабирована ×2 и скрыта
	f["GetSceneItemList|S"]["sceneItems"].([]any)[0].(map[string]any)["sceneItemTransform"] = tf(500, 0, 600, 600, 300, 300, 0, 5)
	f["GetSceneItemList|S"]["sceneItems"].([]any)[0].(map[string]any)["sceneItemEnabled"] = false
	res, _ = Import(f, "S", nil)
	o = res.Layout.Objects[0]
	near(t, "x2", o.Transform.X, 570) // верх-лев. (520,40), размер 200×100, центр (620,90)
	near(t, "y2", o.Transform.Y, 65)
	near(t, "scale2", o.Transform.ScaleX, 2)
	if o.Visible {
		t.Error("скрытая группа → скрытые дети")
	}
}

func TestIsLocalHost(t *testing.T) {
	for _, h := range []string{"localhost", "127.0.0.1", "0.0.0.0", "::1", "[::1]", "app.localhost"} {
		if !IsLocalHost(h) {
			t.Errorf("%s must be local", h)
		}
	}
	for _, h := range []string{"example.com", "192.168.1.5", "127.0.0.1.evil.com"} {
		if IsLocalHost(h) {
			t.Errorf("%s must not be local", h)
		}
	}
}
