package server

// mixerPocPage — страница PoC микшера. ES5 без библиотек (как панель deck), чтобы работала и на старом планшете.
// Полоска уровня — градиент под тёмной «шторкой», у которой меняется только transform: так дёшево и для слабого GPU.
// Поведение как у микшера OBS (имя и дБ, ползунок, mute, прослушивание, шкала уровня), оформление своё.
const mixerPocPage = `<!doctype html>
<html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,user-scalable=no">
<meta name="apple-mobile-web-app-capable" content="yes">
<title>Mixer PoC</title>
<style>
*{box-sizing:border-box;-webkit-tap-highlight-color:transparent}
html,body{margin:0;background:#12151b;color:#e8ebf2;font:15px/1.3 -apple-system,Helvetica,Arial,sans-serif;touch-action:manipulation;-webkit-user-select:none;user-select:none}
header{padding:12px 16px;display:flex;gap:14px;align-items:baseline;flex-wrap:wrap}
h1{font-size:16px;margin:0;font-weight:600}
#st{color:#9aa3b2;font-size:13px}
#st.off{color:#ff8a80}
#rows{padding:0 12px 8px;max-width:760px;margin:0 auto}
.row{display:-webkit-box;display:flex;-webkit-box-orient:vertical;flex-direction:column;background:#1b2029;border-radius:14px;margin-bottom:10px;padding:10px 12px 8px}
.hd{display:-webkit-box;display:flex;-webkit-box-pack:justify;justify-content:space-between;align-items:baseline;margin-bottom:6px}
.nm{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-weight:600;padding-right:8px}
.tg{color:#ffb74d;font-size:12px;margin-right:10px}
.db{color:#9aa3b2;font-size:13px;white-space:nowrap}
.ln{display:-webkit-box;display:flex;align-items:center}
.l1{margin-bottom:6px}
.btn{-webkit-appearance:none;appearance:none;border:0;width:48px;height:40px;margin:0 12px 0 0;border-radius:10px;background:#2a3040;color:#cfd5e2;padding:0;display:-webkit-box;display:flex;align-items:center;justify-content:center;cursor:pointer;-webkit-flex:none;flex:none}
.btn svg{width:24px;height:24px;fill:currentColor;stroke:currentColor;stroke-width:2;stroke-linecap:round;stroke-linejoin:round}
.btn svg .nf{fill:none}
.btn.mu{background:#d9534f;color:#fff}
.btn.both{background:#26a69a;color:#08211f}
.btn.only{background:#ffb74d;color:#2a1b00}
.vol{-webkit-appearance:none;appearance:none;-webkit-box-flex:1;flex:1;min-width:0;height:40px;margin:0;background:transparent}
.vol::-webkit-slider-runnable-track{height:8px;border-radius:4px;background:#3a4256}
.vol::-webkit-slider-thumb{-webkit-appearance:none;width:22px;height:32px;margin-top:-12px;border-radius:6px;background:#f4f6fb;border:0;box-shadow:0 1px 4px rgba(0,0,0,.5)}
.vol::-moz-range-track{height:8px;border-radius:4px;background:#3a4256}
.vol::-moz-range-thumb{width:22px;height:32px;border-radius:6px;background:#f4f6fb;border:0}
.mw{-webkit-box-flex:1;flex:1;min-width:0}
.meter{position:relative;height:14px;border-radius:4px;overflow:hidden;background:#0b0d11}
.meter.muted{opacity:.3}
.zn{position:absolute;top:0;bottom:0}
.zn i{position:absolute;left:0;top:0;right:0;bottom:0;-webkit-transform-origin:0 50%;transform-origin:0 50%}
.zg{background:#1c4a42}.zg i{background:#2fd1b3}
.zy{background:#574814}.zy i{background:#ffd23f}
.zr{background:#5b2124}.zr i{background:#ff4d4d}
.tk{position:absolute;top:0;bottom:0;width:1px;background:rgba(255,255,255,.2)}
.pk{position:absolute;top:0;bottom:0;width:3px;left:0;background:#fff}
.sc{position:relative;height:12px;margin-top:2px;color:#6b7488;font-size:10px}
.sc span{position:absolute;top:0;-webkit-transform:translateX(-50%);transform:translateX(-50%)}
.clip{width:12px;height:12px;border-radius:50%;background:#2a3040;margin:0 0 0 10px;-webkit-flex:none;flex:none}
.clip.hot{background:#ff3b30}
.empty{color:#9aa3b2;padding:24px 4px}
#dg{color:#6b7488;font-size:12px;padding:4px 16px 20px;text-align:center}
</style></head><body>
<header><h1>Mixer PoC</h1><span id="st">connecting…</span></header>
<div id="rows"></div>
<div id="dg"></div>
<script>
(function () {
  var MIN = -60, CLIP = -1, HOLD = 1500, FALL = 40, PFALL = 20, TICKS = [-60, -54, -48, -42, -36, -30, -24, -18, -12, -6, 0];
  var ZG = 20, ZY = 9; // границы цветовых зон шкалы: зелёная до −20 дБ, жёлтая до −9, дальше красная (как в OBS)
  var VSTEPS = 1000, VFLOOR = -100; // ползунок громкости: положение 0…1000, нелинейно, как фейдер OBS
  var rowsEl = document.getElementById("rows"), stEl = document.getElementById("st"), dgEl = document.getElementById("dg");
  var rows = {}, order = "", ws = null, frames = 0, shown = 0, online = false, wsState = "connecting", wsNote = "", count = 0, drawn = 0, drawFps = 0, worst = 0, worstShown = 0;

  var ICON = {
    on: '<svg viewBox="0 0 24 24"><path d="M4 9v6h4l5 4V5L8 9z"/><path class="nf" d="M16.5 8.5a5 5 0 010 7M19 6a8.5 8.5 0 010 12"/></svg>',
    off: '<svg viewBox="0 0 24 24"><path d="M4 9v6h4l5 4V5L8 9z"/><path class="nf" d="M17 9l5 6M22 9l-5 6"/></svg>',
    ph: '<svg viewBox="0 0 24 24"><path class="nf" d="M4 16v-4a8 8 0 0116 0v4"/><rect x="3" y="14" width="5" height="7" rx="1.5"/><rect x="16" y="14" width="5" height="7" rx="1.5"/></svg>'
  };
  var MON_TITLE = { none: "Listen: off", both: "Listen + stream", only: "Listen only (not in stream)" };

  function el(tag, cls, text) { var e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  // Фейдер OBS кубический: mul = положение³, то есть дБ = 60·lg(положение). Первая половина хода (0…−18 дБ) точная, дальше грубее, край — «минус бесконечность» (−100 дБ).
  function dbFromPos(v) { var p = v / VSTEPS; if (p <= 0) return VFLOOR; return Math.min(0, Math.max(VFLOOR, Math.round(60 * Math.log(p) / Math.LN10 * 10) / 10)); }
  function posFromDb(db) { return db <= VFLOOR ? 0 : Math.round(VSTEPS * Math.pow(10, Math.min(0, db) / 60)); }
  function dbText(db) { return db <= VFLOOR ? "-inf dB" : db.toFixed(1) + " dB"; }
  function norm(db) { return Math.max(0, Math.min(1, (db - MIN) / -MIN)); }
  function tx(e, v) { e.style.webkitTransform = v; e.style.transform = v; }
  function send(o) { if (ws && ws.readyState === 1) ws.send(JSON.stringify(o)); }

  function make(name) {
    var r = { name: name, target: MIN, level: MIN, hold: MIN, holdAt: 0, hotAt: 0, dragging: false, lastSend: 0, timer: 0, muted: null, mon: null };
    r.root = el("div", "row");
    var hd = el("div", "hd"), right = el("div", "");
    r.nm = el("div", "nm", name); r.nm.title = name;
    r.tg = el("span", "tg", ""); r.db = el("span", "db", "");
    right.appendChild(r.tg); right.appendChild(r.db); hd.appendChild(r.nm); hd.appendChild(right);

    var l1 = el("div", "ln l1");
    r.mt = el("button", "btn"); r.mt.type = "button";
    r.mt.onclick = function () { send({ t: "toggle", name: name }); };
    r.vol = el("input", "vol"); r.vol.type = "range"; r.vol.min = 0; r.vol.max = VSTEPS; r.vol.step = 1; r.vol.value = VSTEPS;
    function sendNow() { r.lastSend = Date.now(); send({ t: "vol", name: name, db: dbFromPos(+r.vol.value) }); }
    function sendVol() { // не чаще раза в 50 мс, но последнее значение уходит обязательно
      r.db.textContent = dbText(dbFromPos(+r.vol.value));
      var wait = 50 - (Date.now() - r.lastSend);
      clearTimeout(r.timer);
      if (wait <= 0) sendNow(); else r.timer = setTimeout(sendNow, wait);
    }
    r.vol.oninput = function () { r.dragging = true; sendVol(); };
    r.vol.onchange = function () { sendVol(); setTimeout(function () { r.dragging = false; }, 300); };
    l1.appendChild(r.mt); l1.appendChild(r.vol);

    var l2 = el("div", "ln");
    r.ls = el("button", "btn"); r.ls.type = "button"; r.ls.innerHTML = ICON.ph;
    r.ls.onclick = function () { send({ t: "monitor", name: name }); };
    var mw = el("div", "mw");
    r.meter = el("div", "meter"); r.pk = el("div", "pk");
    var zones = [["zg", 0, norm(-ZG)], ["zy", norm(-ZG), norm(-ZY)], ["zr", norm(-ZY), 1]];
    r.fill = [];
    var i, tk, sc = el("div", "sc");
    for (i = 0; i < zones.length; i++) { // у каждой зоны свой тусклый фон и яркая часть, которая растёт вместе с уровнем
      var z = el("div", "zn " + zones[i][0]), fi = el("i", "");
      z.style.left = (zones[i][1] * 100) + "%"; z.style.width = ((zones[i][2] - zones[i][1]) * 100) + "%";
      z.appendChild(fi); r.meter.appendChild(z); r.fill.push(fi);
    }
    for (i = 0; i < TICKS.length; i++) {
      tk = el("div", "tk"); tk.style.left = (norm(TICKS[i]) * 100) + "%"; r.meter.appendChild(tk);
      var lb = el("span", "", String(TICKS[i])); lb.style.left = Math.min(97, Math.max(3, norm(TICKS[i]) * 100)) + "%"; sc.appendChild(lb);
    }
    r.meter.appendChild(r.pk); mw.appendChild(r.meter); mw.appendChild(sc);
    r.clip = el("div", "clip");
    l2.appendChild(r.ls); l2.appendChild(mw); l2.appendChild(r.clip);

    r.root.appendChild(hd); r.root.appendChild(l1); r.root.appendChild(l2);
    return r;
  }

  function apply(inputs) {
    var names = [], i;
    for (i = 0; i < inputs.length; i++) names.push(inputs[i].name);
    var key = names.join("\u0000");
    if (key !== order) { // состав активных источников изменился (например, сменилась сцена) — перестраиваем список
      order = key;
      var next = {}, frag = document.createDocumentFragment();
      for (i = 0; i < names.length; i++) {
        var r0 = rows[names[i]] || make(names[i]);
        next[names[i]] = r0; frag.appendChild(r0.root);
      }
      rows = next;
      rowsEl.innerHTML = "";
      if (!names.length) rowsEl.appendChild(el("div", "empty", "No active audio sources in OBS right now."));
      else rowsEl.appendChild(frag);
    }
    var now = Date.now();
    for (i = 0; i < inputs.length; i++) {
      var f = inputs[i], row = rows[f.name];
      row.target = f.peak;
      if (f.peak > row.hold) { row.hold = f.peak; row.holdAt = now; }
      if (f.peak > CLIP) row.hotAt = now;
      if (row.muted !== f.muted) { // DOM трогаем только при изменении
        row.muted = f.muted;
        row.mt.className = f.muted ? "btn mu" : "btn";
        row.mt.innerHTML = f.muted ? ICON.off : ICON.on;
        row.mt.title = f.muted ? "Unmute" : "Mute";
        row.meter.className = f.muted ? "meter muted" : "meter";
      }
      var mon = f.monitor || "none";
      if (row.mon !== mon) {
        row.mon = mon;
        row.ls.className = mon === "none" ? "btn" : "btn " + mon;
        row.ls.title = MON_TITLE[mon] || "";
        row.tg.textContent = mon === "only" ? "listen only" : (mon === "both" ? "listening" : "");
      }
      if (!row.dragging) { row.vol.value = posFromDb(f.volumeDb); row.db.textContent = dbText(f.volumeDb); }
    }
    count = names.length;
  }

  var prev = 0;
  function draw(ts) {
    var dt = prev ? Math.min(0.1, (ts - prev) / 1000) : 0, now = Date.now();
    if (prev && ts - prev > worst) worst = ts - prev;
    prev = ts; drawn++;
    for (var n in rows) {
      var r = rows[n];
      r.level = r.target >= r.level ? r.target : Math.max(r.target, r.level - FALL * dt); // растёт сразу, падает плавно
      if (now - r.holdAt > HOLD) r.hold = Math.max(r.target, r.hold - PFALL * dt);
      var lv = norm(r.level), g = norm(-ZG), y = norm(-ZY);
      tx(r.fill[0], "scaleX(" + Math.min(1, lv / g).toFixed(3) + ")");
      tx(r.fill[1], "scaleX(" + Math.max(0, Math.min(1, (lv - g) / (y - g))).toFixed(3) + ")");
      tx(r.fill[2], "scaleX(" + Math.max(0, Math.min(1, (lv - y) / (1 - y))).toFixed(3) + ")");
      tx(r.pk, "translateX(" + Math.round(norm(r.hold) * (r.meter.clientWidth - 3)) + "px)");
      var hot = now - r.hotAt < HOLD;
      if (hot !== r.wasHot) { r.clip.className = hot ? "clip hot" : "clip"; r.wasHot = hot; }
    }
    requestAnimationFrame(draw);
  }

  function paintStatus() {
    var msg = wsState === "open" ? (online ? "OBS connected · active sources: " + count : "Connected to the app, but OBS is not connected")
      : (wsState === "connecting" ? "Connecting to the app…" : "No connection to the app, retrying…" + (wsNote ? " (" + wsNote + ")" : ""));
    stEl.className = wsState === "open" && online ? "" : "off";
    stEl.textContent = msg;
    dgEl.textContent = "network " + shown + " frames/s · screen " + drawFps + " fps · worst frame gap " + worstShown + " ms";
  }
  setInterval(function () { shown = frames; frames = 0; drawFps = drawn; drawn = 0; worstShown = Math.round(worst); worst = 0; paintStatus(); }, 1000);

  function connect() {
    ws = new WebSocket((location.protocol === "https:" ? "wss" : "ws") + "://" + location.host + "/api/mixer-poc/ws");
    wsState = "connecting"; paintStatus();
    ws.onopen = function () { wsState = "open"; wsNote = ""; paintStatus(); };
    ws.onerror = function () { wsNote = "error"; };
    ws.onmessage = function (ev) {
      var f; try { f = JSON.parse(ev.data); } catch (e) { return; }
      frames++; online = !!f.online; apply(f.inputs || []);
    };
    ws.onclose = function (ev) { online = false; wsState = "closed"; wsNote = (wsNote ? wsNote + ", " : "") + "code " + (ev && ev.code); paintStatus(); setTimeout(connect, 1000); };
  }
  // Политика безопасности блокирует соединение молча — покажем причину на экране (если браузер сообщает о таком).
  document.addEventListener("securitypolicyviolation", function (e) { wsNote = "blocked: " + e.violatedDirective; paintStatus(); });
  connect();
  requestAnimationFrame(draw);
})();
</script></body></html>`
