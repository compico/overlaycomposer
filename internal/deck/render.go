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
}

type tileView struct {
	ID, Label, Color, Icon, Scene string
	Style                         template.CSS
	Confirm, On                   bool
}

type groupView struct {
	Name  string
	Style template.CSS
	Tiles []tileView
}

type view struct {
	Rev     int
	Groups  []groupView
	Strip   template.CSS
	Online  bool
	Palette template.CSS
	Script  template.JS
}

var page = template.Must(template.New("deck").Parse(pageHTML))

// Размеры в клетках (em от размера клетки, который выставляет скрипт): заголовок группы, отступ слева и промежуток между группами.
const (
	titleCells = 0.5
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
				ID: t.ID, Label: t.Label, Color: t.Color, Icon: Icons[t.Icon], Confirm: t.Confirm,
				Style: template.CSS(fmt.Sprintf("left:%dem;top:%.2fem;width:%dem;height:%dem", t.X, titleCells+float64(t.Y), t.W, t.H)),
				Scene: t.Action.Scene,
			}
			if tv.Color == "" {
				tv.Color = "steel"
			}
			tv.On = v.Online && t.Action.Scene != "" && t.Action.Scene == v.Scene
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
const CSP = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

const pageHTML = `<!doctype html>
<html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,user-scalable=no">
<meta name="apple-mobile-web-app-capable" content="yes">
<title>Deck</title>
<style>
html,body{margin:0;height:100%;background:#111;color:#fff;font-family:"Segoe UI",-apple-system,Helvetica,Arial,sans-serif;overflow:hidden;
 -webkit-user-select:none;-webkit-touch-callout:none;-webkit-tap-highlight-color:rgba(0,0,0,0)}
#scroll{position:absolute;left:0;right:0;top:0;bottom:0;overflow-x:auto;overflow-y:hidden;-webkit-overflow-scrolling:touch;font-size:22vh}
#strip{position:relative;height:100%}
.g{position:absolute;top:0;bottom:0}
.gn{position:absolute;left:.03em;right:0;top:0;height:.5em;line-height:.5em;font-size:.14em;font-weight:300;color:#aaa;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;padding-top:.0em}
.t{position:absolute;display:block;color:#fff;text-decoration:none;-webkit-transition:-webkit-transform .08s;transition:transform .08s}
.t i{position:absolute;left:.03em;right:.03em;top:.03em;bottom:.03em;font-style:normal;overflow:hidden;box-sizing:border-box;border:.025em solid transparent}
.t b{position:absolute;left:.08em;right:.08em;bottom:.06em;font-size:.15em;font-weight:300;line-height:1.15;max-height:3.5em;overflow:hidden;word-wrap:break-word}
.t em{position:absolute;left:.08em;top:.05em;font-size:.26em;font-style:normal;opacity:.9}
.t.on i{border-color:#fff}
.t.arm i{border-color:#e3c800;background-image:-webkit-linear-gradient(135deg,rgba(0,0,0,.25) 25%,transparent 25%,transparent 50%,rgba(0,0,0,.25) 50%,rgba(0,0,0,.25) 75%,transparent 75%);background-size:24px 24px}
.t.busy{opacity:.6}
.t.err i{border-color:#e51400}
.t:active{-webkit-transform:scale(.96);transform:scale(.96)}
body.off .t{opacity:.45}
#dot{position:fixed;right:10px;top:10px;width:12px;height:12px;background:#60a917;z-index:2}
body.off #dot{background:#e51400}
#msg{position:fixed;left:0;right:0;bottom:0;padding:10px 16px;background:#e51400;font-size:18px;display:none;z-index:3}
{{.Palette}}
</style></head>
<body class="{{if not .Online}}off{{end}}" data-rev="{{.Rev}}">
<div id="scroll"><div id="strip" style="{{.Strip}}">{{range .Groups}}<div class="g" style="{{.Style}}"><div class="gn">{{.Name}}</div>{{range .Tiles}}<div class="t c-{{.Color}}{{if .On}} on{{end}}" style="{{.Style}}" data-id="{{.ID}}" data-scene="{{.Scene}}"{{if .Confirm}} data-confirm="1"{{end}}><i>{{if .Icon}}<em>{{.Icon}}</em>{{end}}<b>{{.Label}}</b></i></div>{{end}}</div>{{end}}</div></div>
<span id="dot"></span>
<div id="msg"></div>
<script>{{.Script}}</script>
</body></html>`

// clientJS — ES5, без зависимостей. Нажатие шлёт серверу только id плитки; действие определяет сервер. Опасные плитки (data-confirm)
// взводятся первым касанием и выполняются вторым (3 секунды), сервер повторно проверяет это (409 → взвести). Размер клетки — высота
// экрана / (ряды + строка заголовка): считается при загрузке и повороте, всё остальное в em от него.
const clientJS = `(function(){
var ROWS=4.5;
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
function onTap(el){
 if(el.getAttribute("data-confirm")&&armed!==el){arm(el);return;}
 press(el,armed===el);
}
var tiles=document.querySelectorAll(".t[data-id]");
for(var i=0;i<tiles.length;i++){
 (function(el){el.onclick=function(){onTap(el);};})(tiles[i]);
}
function poll(){
 call("GET","/api/deck/state",null,function(status,j){
  if(status===401){location.href="/pair?next=/deck";return;}
  cls(body,"off",!(status===200&&j&&j.online));
  if(status===200&&j){
   if(String(j.rev)!==rev){
    try{sessionStorage.setItem("deckScroll",String(sc.scrollLeft));}catch(e){}
    location.reload();return;
   }
   for(var k=0;k<tiles.length;k++){
    var s=tiles[k].getAttribute("data-scene");
    cls(tiles[k],"on",!!s&&j.online&&s===j.scene);
   }
  }
  setTimeout(poll,2000);
 });
}
setTimeout(poll,2000);
})();`
