package server

// mixerPocPage — страница PoC микшера. ES5 без библиотек (как панель deck), чтобы проверить и на старом планшете.
// Полоска уровня — градиент под тёмной «шторкой», у которой меняется только transform: так дёшево и для слабого GPU.
const mixerPocPage = `<!doctype html>
<html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Mixer PoC</title>
<style>
body{margin:0;background:#15171c;color:#e6e8ee;font:15px/1.3 sans-serif}
header{padding:12px 16px;border-bottom:1px solid #2a2e38;display:flex;gap:16px;align-items:baseline}
h1{font-size:16px;margin:0;font-weight:600}
#st{color:#9aa3b2;font-size:13px}
#st.off{color:#f87171}
#rows{padding:8px 16px}
.row{display:flex;align-items:center;gap:12px;padding:10px 0;border-bottom:1px solid #20242c}
.nm{width:170px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.mt{width:64px;padding:8px 0;border:0;border-radius:4px;background:#2a2e38;color:#e6e8ee;font:inherit;cursor:pointer}
.mt.on{background:#c0392b}
.mw{flex:1;min-width:160px}
.bar{position:relative;height:18px;background:linear-gradient(to right,#2ecc71 0,#2ecc71 70%,#f1c40f 85%,#e74c3c 100%);overflow:hidden;border-radius:3px}
.bar.muted{opacity:.35}
.cv{position:absolute;left:0;top:0;right:0;bottom:0;background:#0d0f13;-webkit-transform-origin:100% 50%;transform-origin:100% 50%}
.pk{position:absolute;top:0;bottom:0;width:3px;left:0;background:#fff}
.clip{width:14px;height:14px;border-radius:50%;background:#2a2e38}
.clip.hot{background:#ff3b30}
.vol{width:200px}
.db{width:64px;text-align:right;color:#9aa3b2;font-size:13px}
.empty{color:#9aa3b2;padding:24px 0}
</style></head><body>
<header><h1>Mixer PoC</h1><span id="st">connecting…</span></header>
<div id="rows"></div>
<script>
(function () {
  var MIN = -60, VMIN = -60, VMAX = 0, CLIP = -1, HOLD = 1500, FALL = 40, PFALL = 20;
  var rowsEl = document.getElementById("rows"), stEl = document.getElementById("st");
  var rows = {}, order = "", ws = null, frames = 0, shown = 0, online = false, count = 0;

  function el(tag, cls, text) { var e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  function norm(db) { return Math.max(0, Math.min(1, (db - MIN) / -MIN)); }
  function tx(e, v) { e.style.webkitTransform = v; e.style.transform = v; }
  function send(o) { if (ws && ws.readyState === 1) ws.send(JSON.stringify(o)); }

  function make(name) {
    var r = { name: name, level: MIN, target: MIN, hold: MIN, holdAt: 0, hotAt: 0, dragging: false, lastSend: 0 };
    r.root = el("div", "row"); r.nm = el("div", "nm", name); r.nm.title = name;
    r.mt = el("button", "mt", "Mute"); r.mt.type = "button";
    r.mt.onclick = function () { send({ t: "toggle", name: name }); };
    r.bar = el("div", "bar"); r.cv = el("div", "cv"); r.pk = el("div", "pk");
    r.bar.appendChild(r.cv); r.bar.appendChild(r.pk);
    var mw = el("div", "mw"); mw.appendChild(r.bar);
    r.clip = el("div", "clip");
    r.vol = el("input", "vol"); r.vol.type = "range"; r.vol.min = VMIN; r.vol.max = VMAX; r.vol.step = 0.5; r.vol.value = 0;
    r.db = el("div", "db", "");
    function sendVol() { r.lastSend = Date.now(); r.db.textContent = (+r.vol.value).toFixed(1) + " dB"; send({ t: "vol", name: name, db: +r.vol.value }); }
    r.vol.oninput = function () { r.dragging = true; if (Date.now() - r.lastSend > 50) sendVol(); else r.db.textContent = (+r.vol.value).toFixed(1) + " dB"; };
    r.vol.onchange = function () { sendVol(); r.dragging = false; };
    r.root.appendChild(r.nm); r.root.appendChild(r.mt); r.root.appendChild(mw); r.root.appendChild(r.clip); r.root.appendChild(r.vol); r.root.appendChild(r.db);
    return r;
  }

  function apply(inputs) {
    var names = [], i, k;
    for (i = 0; i < inputs.length; i++) names.push(inputs[i].name);
    var key = names.join("\u0000");
    if (key !== order) { // состав активных источников изменился (например, сменилась сцена) — перестраиваем список
      order = key;
      var next = {}, frag = document.createDocumentFragment();
      for (i = 0; i < names.length; i++) {
        var r = rows[names[i]] || make(names[i]);
        next[names[i]] = r; frag.appendChild(r.root);
      }
      rows = next;
      rowsEl.innerHTML = "";
      if (!names.length) rowsEl.appendChild(el("div", "empty", "No active audio sources in OBS right now."));
      else rowsEl.appendChild(frag);
    }
    var now = Date.now();
    for (i = 0; i < inputs.length; i++) {
      var in_ = inputs[i], row = rows[in_.name];
      row.target = in_.peak; row.muted = in_.muted;
      if (in_.peak > row.hold) { row.hold = in_.peak; row.holdAt = now; }
      if (in_.peak > CLIP) row.hotAt = now;
      row.mt.className = in_.muted ? "mt on" : "mt";
      row.mt.textContent = in_.muted ? "Muted" : "Mute";
      row.bar.className = in_.muted ? "bar muted" : "bar";
      if (!row.dragging) { row.vol.value = in_.volumeDb; row.db.textContent = in_.volumeDb.toFixed(1) + " dB"; }
    }
    count = names.length;
  }

  var prev = 0;
  function draw(ts) {
    var dt = prev ? Math.min(0.1, (ts - prev) / 1000) : 0, now = Date.now(); prev = ts;
    for (var n in rows) {
      var r = rows[n];
      r.level = r.target >= r.level ? r.target : Math.max(r.target, r.level - FALL * dt); // растёт сразу, падает плавно
      if (now - r.holdAt > HOLD) r.hold = Math.max(r.target, r.hold - PFALL * dt);
      tx(r.cv, "scaleX(" + (1 - norm(r.level)).toFixed(3) + ")");
      tx(r.pk, "translateX(" + Math.round(norm(r.hold) * (r.bar.clientWidth - 3)) + "px)");
      var hot = now - r.hotAt < HOLD;
      if (hot !== r.wasHot) { r.clip.className = hot ? "clip hot" : "clip"; r.wasHot = hot; }
    }
    requestAnimationFrame(draw);
  }

  function paintStatus() {
    stEl.className = online ? "" : "off";
    stEl.textContent = (online ? "OBS connected" : "OBS is not connected") + " · active sources: " + count + " · " + shown + " frames/s";
  }
  setInterval(function () { shown = frames; frames = 0; paintStatus(); }, 1000);

  function connect() {
    ws = new WebSocket((location.protocol === "https:" ? "wss" : "ws") + "://" + location.host + "/api/mixer-poc/ws");
    ws.onmessage = function (ev) {
      var f; try { f = JSON.parse(ev.data); } catch (e) { return; }
      frames++; online = !!f.online; apply(f.inputs || []);
    };
    ws.onclose = function () { online = false; paintStatus(); setTimeout(connect, 1000); };
  }
  connect();
  requestAnimationFrame(draw);
})();
</script></body></html>`
