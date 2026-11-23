package deck

import (
	"bytes"
	"fmt"
	"html/template"
	"sort"
	"strings"
)

// Страница /deck целиком собирается на сервере: плитки, цвета и подписи уже в HTML. Клиентский код — маленький ES5-скрипт без
// зависимостей (iPad 3 / Safari 9): нажатие → XMLHttpRequest, опрос состояния. Позиции — абсолютные в процентах сетки (flexbox/grid
// в Safari 9 неполные), внутри плитки — отступы. Никаких теней и размытия: слабое железо.

// ViewData — всё, что нужно странице.
type ViewData struct {
	Layout Layout
	Scene  string // активная сцена OBS (подсветка)
	Online bool   // есть ли связь с OBS
	// Muted — заглушённые аудиоисточники OBS: плитки mute для них подсвечены.
	Muted map[string]bool
}

type tileView struct {
	ID, Label, Color, Icon, Scene string
	Style                         template.CSS
	Confirm, On, Plugin, Folder   bool
}

// folderView — оверлей выбора сцены для плитки-папки.
type folderView struct {
	ID     string
	Scenes []string
}

type groupView struct {
	Name  string
	Style template.CSS
	Mixer bool // вместо плиток — микшер; его рисует скрипт по кадрам от сервера
	W     int  // ширина в клетках: скрипт по ней пересчитывает раскладку, если микшер не помещается на экран
	Tiles []tileView
}

type view struct {
	Rev     int
	Groups  []groupView
	Folders []folderView
	Strip   template.CSS
	Online  bool
	Palette template.CSS
	Script  template.JS
}

var page = template.Must(template.New("deck").Parse(pageHTML))

// Размеры в клетках (em от размера клетки, который выставляет скрипт): отступ сверху, заголовок группы, отступ слева и промежуток
// между группами. Скрипт делит высоту экрана на topCells+titleCells+Rows+bottomCells.
const (
	topCells   = 0.2
	titleCells = 0.5
	_          = 0.1 // bottomCells: нижний отступ, учтён только в делителе скрипта (ROWS)
	padCells   = 0.5
	gapCells   = 0.6
)

// Render пишет страницу.
func Render(v ViewData) ([]byte, error) {
	if len(v.Layout.Groups) == 0 {
		return nil, fmt.Errorf("no groups")
	}
	out := view{Rev: v.Layout.Rev, Online: v.Online, Palette: template.CSS(paletteCSS()), Script: template.JS(clientJS)}
	x := padCells
	for _, g := range v.Layout.Groups {
		w := float64(g.Width())
		gv := groupView{Name: g.Name, Mixer: g.Kind == GroupMixer, W: g.Width(), Style: template.CSS(fmt.Sprintf("left:%.2fem;width:%.2fem", x, w))}
		for _, t := range g.Tiles {
			tv := tileView{
				ID: t.ID, Label: t.Label, Color: t.Color, Icon: Icons[t.Icon], Confirm: t.Confirm && t.Action.Type != ActionFolder, Plugin: t.Action.Type == ActionPlugin, Folder: t.Action.Type == ActionFolder,
				Style: template.CSS(fmt.Sprintf("left:%dem;top:%.2fem;width:%dem;height:%dem", t.X, topCells+titleCells+float64(t.Y), t.W, t.H)),
				Scene: t.Action.Scene,
			}
			if tv.Color == "" {
				tv.Color = "steel"
			}
			if tv.Folder {
				out.Folders = append(out.Folders, folderView{ID: t.ID, Scenes: t.Action.Scenes})
			}
			tv.On = v.Online && (t.Action.HasScene(v.Scene) || (t.Action.Type == ActionMute && v.Muted[t.Action.Input]))
			gv.Tiles = append(gv.Tiles, tv)
		}
		out.Groups = append(out.Groups, gv)
		x += w + gapCells
	}
	out.Strip = template.CSS(fmt.Sprintf("width:%.2fem", x-gapCells+padCells))
	var b bytes.Buffer
	if err := page.Execute(&b, out); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func paletteCSS() string {
	names := make([]string, 0, len(Palette))
	for n := range Palette {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		fmt.Fprintf(&sb, ".c-%s i{background:%s}", n, Palette[n]) // цвет на внутреннем блоке: между плитками остаётся зазор
	}
	return sb.String()
}

// CSP страницы: свои стили и скрипт (inline), запросы только на тот же адрес, ничего внешнего.
const CSP = "default-src 'none'; img-src data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// CSPFor — CSP с явным разрешением WebSocket на адрес панели: Safari 9 не считает 'self' достаточным для ws://. host — заголовок Host запроса
// (хост[:порт]); если он выглядит подозрительно, остаётся CSP без WebSocket (панель уйдёт на опрос).
func CSPFor(host string) string {
	if host == "" || len(host) > 255 {
		return CSP
	}
	for _, r := range host {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == ':' || r == '[' || r == ']') {
			return CSP
		}
	}
	return strings.Replace(CSP, "connect-src 'self'", "connect-src 'self' ws://"+host+" wss://"+host, 1)
}

const pageHTML = `<!doctype html>
<html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,user-scalable=no">
<meta name="apple-mobile-web-app-capable" content="yes">
<title>Deck</title>
<link rel="icon" type="image/svg+xml" href="data:image/svg+xml,%3Csvg xmlns=%27http://www.w3.org/2000/svg%27 viewBox=%270 0 256 256%27%3E%3Crect x=%276%27 y=%276%27 width=%27244%27 height=%27244%27 rx=%2756%27 fill=%27%231a1a2e%27/%3E%3Cg stroke=%27%23fff%27 stroke-width=%274%27 stroke-linejoin=%27round%27%3E%3Crect x=%2740%27 y=%2744%27 width=%27122%27 height=%2796%27 rx=%2716%27 fill=%27%238037e8%27/%3E%3Crect x=%2782%27 y=%2788%27 width=%27118%27 height=%2792%27 rx=%2716%27 fill=%27%2306b6d4%27/%3E%3Crect x=%2758%27 y=%27132%27 width=%27108%27 height=%2772%27 rx=%2716%27 fill=%27%23fb923c%27/%3E%3C/g%3E%3C/svg%3E">
<style>
html,body{margin:0;height:100%;background:#111;color:#fff;font-family:"Segoe UI",-apple-system,Helvetica,Arial,sans-serif;overflow:hidden;
 -webkit-user-select:none;-webkit-touch-callout:none;-webkit-tap-highlight-color:rgba(0,0,0,0)}
#scroll{position:absolute;left:0;right:0;top:0;bottom:0;overflow-x:auto;overflow-y:hidden;-webkit-overflow-scrolling:touch;font-size:22vh}
#strip{position:relative;height:100%}
.g{position:absolute;top:0;bottom:0}
.gn{position:absolute;left:.03em;right:0;top:.2em;height:.5em}
.gn span{position:absolute;left:0;right:0;bottom:.04em;font-size:.15em;line-height:1.3;font-weight:300;color:#aaa;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.t{position:absolute;display:block;color:#fff;text-decoration:none;-webkit-transition:-webkit-transform .08s;transition:transform .08s}
.t i{position:absolute;left:.03em;right:.03em;top:.03em;bottom:.03em;font-style:normal;overflow:hidden;box-sizing:border-box;border:.025em solid transparent}
.t b{position:absolute;left:.08em;right:.08em;bottom:.06em;font-size:.15em;font-weight:300;line-height:1.15;max-height:3.5em;overflow:hidden;word-wrap:break-word}
.t em{position:absolute;left:.08em;top:.05em;font-size:.26em;font-style:normal;opacity:.9}
.t.on i{border-color:#fff}
.t s{position:absolute;left:.1em;right:.1em;top:.45em;bottom:.5em;text-decoration:none;overflow:hidden;text-align:center;font-size:.15em;line-height:1.3}
.t s div{font-weight:300;opacity:.9;margin-top:.2em;overflow:hidden;text-overflow:ellipsis}
.t s div:first-child{font-weight:600;font-size:1.25em;opacity:1;margin-top:0}
.t.arm i{border-color:#e3c800;background-image:-webkit-linear-gradient(135deg,rgba(0,0,0,.25) 25%,transparent 25%,transparent 50%,rgba(0,0,0,.25) 50%,rgba(0,0,0,.25) 75%,transparent 75%);background-size:24px 24px}
.t.busy{opacity:.6}
.t.err i{border-color:#e51400}
.t:active{-webkit-transform:scale(.96);transform:scale(.96)}
body.off .t{opacity:.45}
#fo{position:fixed;left:0;right:0;top:0;bottom:0;background:rgba(0,0,0,.75);display:none;z-index:4;overflow-y:auto;-webkit-overflow-scrolling:touch}
.fl{display:none;margin:6% auto;width:80%;max-width:560px}
.fs{margin:0 0 10px;padding:18px 20px;background:#647687;font-size:22px;font-weight:300;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;border:3px solid transparent}
.fs.on{border-color:#fff}
.fs:active{background:#7b8c9c}
#dot{position:fixed;right:10px;top:10px;width:12px;height:12px;background:#60a917;z-index:2}
body.off #dot{background:#e51400}
#msg{position:fixed;left:0;right:0;bottom:0;padding:10px 16px;background:#e51400;font-size:18px;display:none;z-index:3}
body.off .mx{opacity:.45}
.mx{position:absolute;left:.03em;right:.03em;top:.7em;height:4em;overflow-x:hidden;overflow-y:auto;-webkit-overflow-scrolling:touch}
.mx button,.mx input{font:inherit}
.mxe{padding:.15em .05em;font-size:.13em;color:#8a93a3}
.mr{position:relative;height:.9em;margin-bottom:.05em;background:#1b2029;overflow:hidden}
/* Текст и значки позиционируем абсолютно: иначе крупный шрифт клетки создаёт огромную строку, и содержимое уезжает вниз (у кнопок font-size:0 — текста в них нет) */
.mn{position:absolute;left:.1em;right:1.7em;top:.02em;height:.22em}
.mn span{position:absolute;left:0;right:0;top:50%;margin-top:-.62em;font-size:.11em;line-height:1.25;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.mt{position:absolute;right:.1em;top:.02em;width:1.6em;height:.22em}
.mt span{position:absolute;left:0;right:0;top:50%;margin-top:-.62em;font-size:.1em;line-height:1.25;color:#9aa3b2;white-space:nowrap;text-align:right}
.mt.on span{color:#ffb74d}
.mb{position:absolute;left:.06em;width:.42em;height:.3em;padding:0;border:0;margin:0;background:#2a3040;color:#cfd5e2;-webkit-appearance:none;appearance:none;font-size:0;line-height:0}
.mb svg{position:absolute;left:50%;top:50%;width:.2em;height:.2em;margin:-.1em 0 0 -.1em;fill:currentColor;stroke:currentColor;stroke-width:2;stroke-linecap:round;stroke-linejoin:round}
.mb svg .nf{fill:none}
.mb.mu{background:#d9534f;color:#fff}.mb.both{background:#26a69a;color:#08211f}.mb.only{background:#ffb74d;color:#2a1b00}
.mv{position:absolute;left:.54em;right:.06em;top:.25em;height:.3em;margin:0;background:transparent;-webkit-appearance:none;appearance:none}
.mv::-webkit-slider-runnable-track{height:.07em;border-radius:.03em;background:#3a4256}
.mv::-webkit-slider-thumb{-webkit-appearance:none;width:.15em;height:.26em;margin-top:-.095em;border-radius:.04em;background:#f4f6fb;border:0}
.mv::-moz-range-track{height:.07em;border-radius:.03em;background:#3a4256}
.mv::-moz-range-progress{background:transparent}
.mv::-moz-range-thumb{box-sizing:border-box;width:.15em;height:.26em;border-radius:.04em;background:#f4f6fb;border:0}
.mv::-moz-focus-outer{border:0}
.mm{position:absolute;left:.54em;right:.22em;top:.6em;height:.12em;overflow:hidden;background:#0b0d11}
.mm.muted{opacity:.3}
.zn{position:absolute;top:0;bottom:0}
.zn i{position:absolute;left:0;top:0;right:0;bottom:0;-webkit-transform-origin:0 50%;transform-origin:0 50%}
.zg{background:#1c4a42}.zg i{background:#2fd1b3}.zy{background:#574814}.zy i{background:#ffd23f}.zr{background:#5b2124}.zr i{background:#ff4d4d}
.tk{position:absolute;top:0;bottom:0;width:1px;background:rgba(255,255,255,.2)}
.pk{position:absolute;top:0;bottom:0;width:.02em;left:0;background:#fff}
.ms{position:absolute;left:.54em;right:.22em;top:.73em;height:.14em}
.ms span{position:absolute;top:0;font-size:.06em;color:#6b7488;-webkit-transform:translateX(-50%);transform:translateX(-50%)}
.mc{position:absolute;right:.06em;top:.64em;width:.08em;height:.08em;border-radius:50%;background:#2a3040}.mc.hot{background:#ff3b30}
{{.Palette}}
</style></head>
<body class="{{if not .Online}}off{{end}}" data-rev="{{.Rev}}">
<div id="scroll"><div id="strip" style="{{.Strip}}">{{range .Groups}}<div class="g" style="{{.Style}}" data-w="{{.W}}"{{if .Mixer}} data-mx="1"{{end}}><div class="gn"><span>{{.Name}}</span></div>{{if .Mixer}}<div class="mx" id="mx"><div class="mxe">No active audio sources</div></div>{{end}}{{range .Tiles}}<div class="t c-{{.Color}}{{if .On}} on{{end}}" style="{{.Style}}" data-id="{{.ID}}" data-scene="{{.Scene}}"{{if .Folder}} data-folder="1"{{end}}{{if .Confirm}} data-confirm="1"{{end}}><i>{{if .Icon}}<em>{{.Icon}}</em>{{end}}{{if or .Plugin .Folder}}<s></s>{{end}}<b>{{.Label}}</b></i></div>{{end}}</div>{{end}}</div></div>
<div id="fo">{{range .Folders}}<div class="fl" data-for="{{.ID}}">{{range .Scenes}}<div class="fs" data-scene="{{.}}">{{.}}</div>{{end}}</div>{{end}}</div>
<span id="dot"></span>
<div id="msg"></div>
<script>{{.Script}}</script>
</body></html>`

// clientJS — ES5, без зависимостей. Нажатие шлёт серверу только id плитки; действие определяет сервер. Опасные плитки (data-confirm)
// взводятся первым касанием и выполняются вторым (3 секунды), сервер повторно проверяет это (409 → взвести). Размер клетки — высота
// экрана / (ряды + строка заголовка): считается при загрузке и повороте, всё остальное в em от него.
const clientJS = `(function(){
var ROWS=4.8;
var body=document.body,sc=document.getElementById("scroll"),rev=body.getAttribute("data-rev"),armed=null,armTimer=0,msgTimer=0,msg=document.getElementById("msg");
var PAD=0.5,GAP=0.6;
/* Размер клетки задаёт высота экрана, а на узком экране (iPad в портрете) заданная ширина микшера в клетках шире самого экрана: сжимаем только микшер до ширины экрана и сдвигаем следующие группы */
function layout(){
 var cell=sc.clientHeight/ROWS,gs=sc.getElementsByClassName("g"),st=document.getElementById("strip"),x=PAD,i,w,g,room;
 if(!cell){return;}
 room=sc.clientWidth/cell-2*PAD;
 for(i=0;i<gs.length;i++){
  g=gs[i];w=parseFloat(g.getAttribute("data-w"))||1;
  if(g.getAttribute("data-mx")&&w>room){w=Math.max(room,2);}
  g.style.left=x+"em";g.style.width=w+"em";x+=w+GAP;
 }
 st.style.width=(x-GAP+PAD)+"em";
}
function fit(){sc.style.fontSize=(sc.clientHeight/ROWS)+"px";layout();}
fit();
window.onresize=fit;window.onorientationchange=function(){setTimeout(fit,150);};
try{var x0=sessionStorage.getItem("deckScroll");if(x0){sc.scrollLeft=parseFloat(x0)||0;}}catch(e){}
function call(method,url,data,cb){
 var x=new XMLHttpRequest();x.open(method,url);x.timeout=6000;
 x.setRequestHeader("X-Overlay-Composer","1");
 if(data){x.setRequestHeader("Content-Type","application/json");}
 x.onreadystatechange=function(){
  if(x.readyState!==4){return;}
  var j=null;try{j=JSON.parse(x.responseText);}catch(e){}
  cb(x.status,j);
 };
 x.send(data?JSON.stringify(data):null);
}
function say(text){
 msg.textContent=text;msg.style.display="block";
 clearTimeout(msgTimer);msgTimer=setTimeout(function(){msg.style.display="none";},4000);
}
function cls(el,name,on){
 var c=" "+el.className+" ",has=c.indexOf(" "+name+" ")>=0;
 if(on&&!has){el.className=el.className+" "+name;}
 if(!on&&has){el.className=c.replace(" "+name+" "," ").replace(/^ +| +$/g,"");}
}
function disarm(){
 if(armed){cls(armed,"arm",false);}
 armed=null;clearTimeout(armTimer);
}
function arm(el){
 disarm();armed=el;cls(el,"arm",true);
 armTimer=setTimeout(disarm,3000);
}
function press(el,confirmed){
 var id=el.getAttribute("data-id");
 cls(el,"busy",true);
 call("POST","/api/deck/press",{tile:id,confirm:confirmed},function(status,j){
  cls(el,"busy",false);
  if(status===409&&j&&j.confirm){arm(el);return;}
  if(status===200){disarm();return;}
  cls(el,"err",true);setTimeout(function(){cls(el,"err",false);},1200);
  say((j&&j.error)||(status===0?"No connection":"Error "+status));
 });
}
var fo=document.getElementById("fo"),openFolder="";
function closeFolder(){
 fo.style.display="none";
 var ls=fo.children;for(var i=0;i<ls.length;i++){ls[i].style.display="none";}
 openFolder="";
}
function showFolder(id){
 var ls=fo.children,found=false;
 for(var i=0;i<ls.length;i++){
  var on=ls[i].getAttribute("data-for")===id;
  ls[i].style.display=on?"block":"none";
  if(on){found=true;}
 }
 if(found){openFolder=id;fo.style.display="block";}
}
fo.onclick=function(ev){
 var t=ev.target||ev.srcElement;
 if(t===fo||t.className.indexOf("fl")>=0&&t.className.indexOf("fs")<0){closeFolder();return;}
 var scene=t.getAttribute&&t.getAttribute("data-scene");
 if(!scene||!openFolder){return;}
 var id=openFolder;
 call("POST","/api/deck/press",{tile:id,scene:scene},function(status,j){
  if(status===200){closeFolder();return;}
  say((j&&j.error)||(status===0?"No connection":"Error "+status));
 });
};
function onTap(el){
 if(el.getAttribute("data-folder")){showFolder(el.getAttribute("data-id"));return;}
 if(el.getAttribute("data-confirm")&&armed!==el){arm(el);return;}
 press(el,armed===el);
}
var tiles=document.querySelectorAll(".t[data-id]");
for(var i=0;i<tiles.length;i++){
 (function(el){el.onclick=function(){onTap(el);};})(tiles[i]);
}
function setText(box,lines){
 var key=lines.join("\n");
 if(box.getAttribute("data-k")===key){return;}
 box.setAttribute("data-k",key);
 while(box.firstChild){box.removeChild(box.firstChild);}
 for(var i=0;i<lines.length;i++){
  var d=document.createElement("div");d.appendChild(document.createTextNode(lines[i]));box.appendChild(d);
 }
}
function apply(j){
 cls(body,"off",!j.online);
 if(String(j.rev)!==rev){
  try{sessionStorage.setItem("deckScroll",String(sc.scrollLeft));}catch(e){}
  location.reload();return;
 }
 var on=j.on,map=null;
 if(on){map={};for(var m=0;m<on.length;m++){map[on[m]]=1;}}
 var fs=fo.getElementsByTagName("div");
 for(var q=0;q<fs.length;q++){
  var fsc=fs[q].getAttribute("data-scene");
  if(fsc!==null){cls(fs[q],"on",!!j.online&&fsc===j.scene);}
 }
 var tx=j.text||{};
 for(var k=0;k<tiles.length;k++){
  var el=tiles[k],s=el.getAttribute("data-scene"),id=el.getAttribute("data-id");
  cls(el,"on",map?!!map[id]:(!!s&&j.online&&s===j.scene));
  var box=el.getElementsByTagName("s")[0];
  if(box){setText(box,tx[id]||[]);}
 }
}
var ws=null,wsUp=false,wsTry=0,polling=false;
function startPoll(delay){
 if(polling||wsUp){return;}
 polling=true;setTimeout(poll,delay);
}
function poll(){
 if(wsUp){polling=false;return;}
 call("GET","/api/deck/state",null,function(status,j){
  if(status===401){location.href="/pair?next=/deck";return;}
  if(status===200&&j){apply(j);}else{cls(body,"off",true);}
  setTimeout(poll,2000);
 });
}
function connect(){
 if(!window.WebSocket){return;}
 var opened=false;
 try{ws=new WebSocket((location.protocol==="https:"?"wss://":"ws://")+location.host+"/api/deck/ws");}catch(e){ws=null;return;}
 ws.onopen=function(){opened=true;wsUp=true;wsTry=0;mxSub=false;mxWant(!mxHidden());};
 ws.onmessage=function(ev){
  var j=null;try{j=JSON.parse(ev.data);}catch(e){}
  if(j&&j.t==="state"){apply(j);}
  else if(j&&j.t==="mixer"){mxApply(j.inputs||[]);}
 };
 ws.onclose=function(){
  wsUp=false;ws=null;mxSub=false;if(mxEl){mxNote("Waiting for the connection…");}
  startPoll(opened?0:2000);
  wsTry++;
  setTimeout(connect,Math.min(30000,2000*wsTry));
 };
}
var mxEl=document.getElementById("mx"),mxRows={},mxOrder="",mxSub=false,mxRaf=false,mxPrev=0;
var MX_MIN=-60,MX_CLIP=-1,MX_HOLD=1500,MX_FALL=40,MX_PFALL=20,MX_TICKS=[-60,-48,-36,-24,-12,0],MX_ZG=20,MX_ZY=9,MX_STEPS=1000,MX_FLOOR=-100;
var MX_ICON={on:'<svg viewBox="0 0 24 24"><path d="M4 9v6h4l5 4V5L8 9z"/><path class="nf" d="M16.5 8.5a5 5 0 010 7M19 6a8.5 8.5 0 010 12"/></svg>',
 off:'<svg viewBox="0 0 24 24"><path d="M4 9v6h4l5 4V5L8 9z"/><path class="nf" d="M17 9l5 6M22 9l-5 6"/></svg>',
 ph:'<svg viewBox="0 0 24 24"><path class="nf" d="M4 16v-4a8 8 0 0116 0v4"/><rect x="3" y="14" width="5" height="7" rx="1.5"/><rect x="16" y="14" width="5" height="7" rx="1.5"/></svg>'};
var raf=window.requestAnimationFrame||window.webkitRequestAnimationFrame||function(f){return setTimeout(function(){f(Date.now());},33);};
function mxHidden(){return document.hidden||document.webkitHidden;}
function mxSend(o){if(ws&&wsUp){try{ws.send(JSON.stringify(o));}catch(e){}}}
function mxNode(tag,cls,text){var e=document.createElement(tag);if(cls){e.className=cls;}if(text!=null){e.appendChild(document.createTextNode(text));}return e;}
function mxNorm(db){return Math.max(0,Math.min(1,(db-MX_MIN)/-MX_MIN));}
function mxTx(e,v){e.style.webkitTransform=v;e.style.transform=v;}
// Фейдер OBS кубический: дБ = 60·lg(положение). Первая половина хода точная (0…−18 дБ), дальше грубее, край — «минус бесконечность» (−100 дБ).
function mxDbFromPos(v){var p=v/MX_STEPS;if(p<=0){return MX_FLOOR;}return Math.min(0,Math.max(MX_FLOOR,Math.round(60*Math.log(p)/Math.LN10*10)/10));}
function mxPosFromDb(db){return db<=MX_FLOOR?0:Math.round(MX_STEPS*Math.pow(10,Math.min(0,db)/60));}
function mxDbText(db){return db<=MX_FLOOR?"-inf dB":db.toFixed(1)+" dB";}
function mxNote(text){
 if(!mxEl){return;}
 mxRows={};mxOrder="";
 while(mxEl.firstChild){mxEl.removeChild(mxEl.firstChild);}
 mxEl.appendChild(mxNode("div","mxe",text));
}
function mxMake(name){
 var r={name:name,target:MX_MIN,level:MX_MIN,hold:MX_MIN,holdAt:0,hotAt:0,dragging:false,lastSend:0,timer:0,muted:null,mon:null,wasHot:null};
 r.root=mxNode("div","mr");
 r.nm=mxNode("div","mn");r.nmt=mxNode("span","",name);r.nm.appendChild(r.nmt);
 r.tt=mxNode("div","mt");r.db=mxNode("span","","");r.tt.appendChild(r.db);r.tag="";
 r.show=function(db){r.db.firstChild.nodeValue=(r.tag?r.tag+"  ":"")+mxDbText(db);}; // тег прослушивания и громкость — одной строкой
 r.mt=mxNode("button","mb");r.mt.style.top=".25em";r.mt.onclick=function(){mxSend({t:"mx",a:"toggle",n:name});};
 r.vol=mxNode("input","mv");r.vol.type="range";r.vol.min=0;r.vol.max=MX_STEPS;r.vol.step=1;r.vol.value=MX_STEPS;
 function sendNow(){r.lastSend=Date.now();mxSend({t:"mx",a:"vol",n:name,db:mxDbFromPos(+r.vol.value)});}
 function sendVol(){ // не чаще раза в 50 мс, но последнее значение уходит обязательно
  r.show(mxDbFromPos(+r.vol.value));
  var wait=50-(Date.now()-r.lastSend);
  clearTimeout(r.timer);
  if(wait<=0){sendNow();}else{r.timer=setTimeout(sendNow,wait);}
 }
 r.vol.oninput=function(){r.dragging=true;sendVol();};
 r.vol.onchange=function(){sendVol();setTimeout(function(){r.dragging=false;},300);};
 // пока палец на ползунке, полоса не должна уезжать вбок вместе с группой
 r.vol.ontouchstart=function(){sc.style.overflowX="hidden";};
 r.vol.ontouchend=r.vol.ontouchcancel=function(){sc.style.overflowX="auto";};
 r.ls=mxNode("button","mb");r.ls.style.top=".57em";r.ls.innerHTML=MX_ICON.ph;r.ls.onclick=function(){mxSend({t:"mx",a:"monitor",n:name});};
 r.meter=mxNode("div","mm");r.pk=mxNode("div","pk");
 var zones=[["zg",0,mxNorm(-MX_ZG)],["zy",mxNorm(-MX_ZG),mxNorm(-MX_ZY)],["zr",mxNorm(-MX_ZY),1]],i;
 r.fill=[];
 for(i=0;i<zones.length;i++){
  var z=mxNode("div","zn "+zones[i][0]),fi=mxNode("i","");
  z.style.left=(zones[i][1]*100)+"%";z.style.width=((zones[i][2]-zones[i][1])*100)+"%";
  z.appendChild(fi);r.meter.appendChild(z);r.fill.push(fi);
 }
 var sc2=mxNode("div","ms");
 for(i=0;i<MX_TICKS.length;i++){
  var tk=mxNode("div","tk");tk.style.left=(mxNorm(MX_TICKS[i])*100)+"%";r.meter.appendChild(tk);
  var lb=mxNode("span","",String(MX_TICKS[i]));lb.style.left=Math.min(96,Math.max(4,mxNorm(MX_TICKS[i])*100))+"%";sc2.appendChild(lb);
 }
 r.meter.appendChild(r.pk);r.clip=mxNode("div","mc");
 r.root.appendChild(r.nm);r.root.appendChild(r.tt);r.root.appendChild(r.mt);r.root.appendChild(r.vol);r.root.appendChild(r.ls);r.root.appendChild(r.meter);r.root.appendChild(sc2);r.root.appendChild(r.clip);
 return r;
}
function mxApply(inputs){
 if(!mxEl){return;}
 var names=[],i;
 for(i=0;i<inputs.length;i++){names.push(inputs[i].name);}
 var key=names.join("\u0000");
 if(key!==mxOrder){ // состав активных источников изменился (например, сменилась сцена) — перестраиваем список
  mxOrder=key;
  var next={},frag=document.createDocumentFragment();
  for(i=0;i<names.length;i++){var r0=mxRows[names[i]]||mxMake(names[i]);next[names[i]]=r0;frag.appendChild(r0.root);}
  mxRows=next;
  while(mxEl.firstChild){mxEl.removeChild(mxEl.firstChild);}
  if(!names.length){mxEl.appendChild(mxNode("div","mxe","No active audio sources"));}else{mxEl.appendChild(frag);}
 }
 var now=Date.now();
 for(i=0;i<inputs.length;i++){
  var f=inputs[i],row=mxRows[f.name];
  row.target=f.peak;
  if(f.peak>row.hold){row.hold=f.peak;row.holdAt=now;}
  if(f.peak>MX_CLIP){row.hotAt=now;}
  if(row.muted!==f.muted){ // DOM трогаем только при изменении
   row.muted=f.muted;
   row.mt.className=f.muted?"mb mu":"mb";
   row.mt.innerHTML=f.muted?MX_ICON.off:MX_ICON.on;
   row.meter.className=f.muted?"mm muted":"mm";
  }
  var mon=f.monitor||"none";
  if(row.mon!==mon){
   row.mon=mon;
   row.ls.className=mon==="none"?"mb":"mb "+mon;
   row.tag=mon==="only"?"listen only":(mon==="both"?"listening":"");
   row.tt.className=mon==="none"?"mt":"mt on";
   row.shown=null;
  }
  if(!row.dragging&&(row.shown!==f.volumeDb)){row.shown=f.volumeDb;row.vol.value=mxPosFromDb(f.volumeDb);row.show(f.volumeDb);}
 }
}
function mxDraw(ts){
 var dt=mxPrev?Math.min(0.1,(ts-mxPrev)/1000):0,now=Date.now();mxPrev=ts;
 for(var n in mxRows){
  var r=mxRows[n];
  r.level=r.target>=r.level?r.target:Math.max(r.target,r.level-MX_FALL*dt); // растёт сразу, падает плавно
  if(now-r.holdAt>MX_HOLD){r.hold=Math.max(r.target,r.hold-MX_PFALL*dt);}
  var lv=mxNorm(r.level),g=mxNorm(-MX_ZG),y=mxNorm(-MX_ZY);
  mxTx(r.fill[0],"scaleX("+Math.min(1,lv/g).toFixed(3)+")");
  mxTx(r.fill[1],"scaleX("+Math.max(0,Math.min(1,(lv-g)/(y-g))).toFixed(3)+")");
  mxTx(r.fill[2],"scaleX("+Math.max(0,Math.min(1,(lv-y)/(1-y))).toFixed(3)+")");
  mxTx(r.pk,"translateX("+Math.round(mxNorm(r.hold)*(r.meter.clientWidth-2))+"px)");
  var hot=now-r.hotAt<MX_HOLD;
  if(hot!==r.wasHot){r.clip.className=hot?"mc hot":"mc";r.wasHot=hot;}
 }
}
function mxTick(ts){
 if(!mxSub){mxRaf=false;return;}
 mxDraw(ts||Date.now());
 raf.call(window,mxTick);
}
// Подписка на уровни включена, только пока микшер виден: в фоне вкладки (iPad свернул Safari) сервер не шлёт кадры.
function mxWant(on){
 if(!mxEl||on===mxSub||(on&&!wsUp)){return;}
 mxSub=on;mxSend({t:"mixer",on:on});
 if(on&&!mxRaf){mxRaf=true;raf.call(window,mxTick);}
}
if(mxEl){
 var mxVis=function(){mxWant(!mxHidden());};
 document.addEventListener("visibilitychange",mxVis);document.addEventListener("webkitvisibilitychange",mxVis);
}

connect();
startPoll(2000);
})();`
