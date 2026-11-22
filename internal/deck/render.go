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
		gv := groupView{Name: g.Name, Style: template.CSS(fmt.Sprintf("left:%.2fem;width:%.2fem", x, w))}
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
{{.Palette}}
</style></head>
<body class="{{if not .Online}}off{{end}}" data-rev="{{.Rev}}">
<div id="scroll"><div id="strip" style="{{.Strip}}">{{range .Groups}}<div class="g" style="{{.Style}}"><div class="gn"><span>{{.Name}}</span></div>{{range .Tiles}}<div class="t c-{{.Color}}{{if .On}} on{{end}}" style="{{.Style}}" data-id="{{.ID}}" data-scene="{{.Scene}}"{{if .Folder}} data-folder="1"{{end}}{{if .Confirm}} data-confirm="1"{{end}}><i>{{if .Icon}}<em>{{.Icon}}</em>{{end}}{{if or .Plugin .Folder}}<s></s>{{end}}<b>{{.Label}}</b></i></div>{{end}}</div>{{end}}</div></div>
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
function fit(){sc.style.fontSize=(sc.clientHeight/ROWS)+"px";}
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
 ws.onopen=function(){opened=true;wsUp=true;wsTry=0;};
 ws.onmessage=function(ev){
  var j=null;try{j=JSON.parse(ev.data);}catch(e){}
  if(j&&j.t==="state"){apply(j);}
 };
 ws.onclose=function(){
  wsUp=false;ws=null;
  startPoll(opened?0:2000);
  wsTry++;
  setTimeout(connect,Math.min(30000,2000*wsTry));
 };
}
connect();
startPoll(2000);
})();`
