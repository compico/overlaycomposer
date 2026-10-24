package proxy

import (
	"encoding/json"
	"net/url"
	"strings"
)

// Скрипт выполняется внутри проксируемой страницы: перенаправляет fetch/XHR/EventSource/WebSocket на наши точки
// /proxy/, /proxy-h/, /proxy-ws/ и скрывает frameElement. window.top/parent переопределить нельзя — проверки с ними
// переписывает сервер (rewrite.NeutralizeFrameChecks).
const clientPatchJS = `(function(cfg){
  var P="/proxy/"+cfg.id, w=window;
  try{Object.defineProperty(window,"frameElement",{get:function(){return null},configurable:true})}catch(e){}
  // SPA с роутером без basename (маршруты "/r/:name") не узнаёт адрес /proxy/<id>/r/x и молча рисует пустую страницу:
  // показываем ей путь без нашего префикса. Запросы всё равно идут через toProxy (root-relative → /proxy/<id>/…).
  if(cfg.spa){try{
    var lp=location.pathname;
    if(lp===P||lp.indexOf(P+"/")===0) history.replaceState(history.state,"",(lp.slice(P.length)||"/")+location.search+location.hash);
  }catch(e){}}
  function inSite(h){return h===cfg.site||h.slice(-(cfg.site.length+1))==="."+cfg.site}
  function toProxy(u){
    try{
      var a=new URL(u,location.href), mine=a.origin===location.origin;
      // Адрес сайта или root-relative путь (/api/…, он разрешается в наш origin) → через /proxy/<id>/, чтобы сработали cookie этого пути.
      if(a.origin===cfg.origin||(mine&&!/^\/proxy(?:-[a-z]+)?\//.test(a.pathname))) return P+a.pathname+a.search+a.hash;
      // Соседний хост того же сайта (socket8.example.com): через /proxy-h/, Origin подставит сервер.
      if(!mine&&(a.protocol==="http:"||a.protocol==="https:")&&inSite(a.hostname)) return "/proxy-h/"+cfg.id+"/"+a.protocol.slice(0,-1)+"/"+a.host+a.pathname+a.search+a.hash;
    }catch(e){}
    return u;
  }
  // Динамически созданные <script src="/zaraz/s.js">, <link href="/assets/x.css">, <img src="/a.png">: root-relative адрес
  // разрешился бы в корень нашего сервера — добавляем /proxy/<id>. Трекеры из списка cfg.block не загружаем вовсе.
  function blocked(u){
    try{var h=new URL(u,location.href).hostname;return (cfg.block||[]).some(function(b){return h===b||h.slice(-(b.length+1))==="."+b})}catch(e){return false}
  }
  function fixURL(v,tag){
    if(typeof v!=="string") return v;
    if(blocked(v)) return tag==="SCRIPT"?"data:text/javascript,":"data:,";
    if(v.charAt(0)==="/"&&v.charAt(1)!=="/"&&v.charAt(1)!=="\\"&&!/^\/proxy(?:-[a-z]+)?\//.test(v)) return P+v;
    return v;
  }
  [["HTMLScriptElement","src"],["HTMLLinkElement","href"],["HTMLImageElement","src"],["HTMLIFrameElement","src"],
   ["HTMLSourceElement","src"],["HTMLMediaElement","src"],["HTMLEmbedElement","src"]].forEach(function(p){
    try{
      var C=w[p[0]]; if(!C) return;
      var d=Object.getOwnPropertyDescriptor(C.prototype,p[1]); if(!d||!d.set) return;
      Object.defineProperty(C.prototype,p[1],{get:d.get,set:function(v){d.set.call(this,fixURL(String(v),this.tagName))},configurable:true,enumerable:d.enumerable});
    }catch(e){}
  });
  var _sa=Element.prototype.setAttribute;
  Element.prototype.setAttribute=function(n,v){
    if(typeof n==="string"&&this.tagName!=="A"){
      var l=n.toLowerCase();
      if(l==="src"||l==="href") v=fixURL(String(v),this.tagName);
    }
    return _sa.call(this,n,v);
  };
  var _fetch=w.fetch;
  if(_fetch) w.fetch=function(input,init){
    if(typeof input==="string") input=toProxy(input);
    else if(input instanceof URL) input=toProxy(input.href);
    else if(input&&typeof input.url==="string"){var nu=toProxy(input.url); if(nu!==input.url) input=new Request(nu,input);}
    return _fetch.call(this,input,init);
  };
  var _open=XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open=function(m,u){
    var args=Array.prototype.slice.call(arguments); args[1]=toProxy(String(u));
    return _open.apply(this,args);
  };
  if(w.EventSource){
    var _ES=w.EventSource;
    var ES=function(url,init){return new _ES(toProxy(String(url)),init)};
    ES.prototype=_ES.prototype;
    ["CONNECTING","OPEN","CLOSED"].forEach(function(k){ES[k]=_ES[k]});
    w.EventSource=ES;
  }
  var _WS=w.WebSocket;
  var WS=function(url,protocols){
    var u=String(url);
    try{
      var a=new URL(u,location.href), proto=a.protocol.replace(/^http/,"ws");
      if(proto==="ws:"||proto==="wss:"){
        var mine=a.host===location.host;
        if(mine?!/^\/proxy/.test(a.pathname):inSite(a.hostname)){
          var scheme=mine?cfg.wsScheme:proto.slice(0,-1), host=mine?cfg.host:a.host;
          u=(location.protocol==="https:"?"wss":"ws")+"://"+location.host+"/proxy-ws/"+cfg.id+"/"+scheme+"/"+host+a.pathname+a.search;
        }
      }
    }catch(e){}
    return protocols===undefined?new _WS(u):new _WS(u,protocols);
  };
  WS.prototype=_WS.prototype;
  ["CONNECTING","OPEN","CLOSING","CLOSED"].forEach(function(k){WS[k]=_WS[k]});
  w.WebSocket=WS;
})`

// BlockedHosts — трекеры и счётчики, которые страница не должна загружать (в виджете они бесполезны,
// а у mytopf.com ещё и просрочен сертификат — в консоли сплошные ошибки).
var BlockedHosts = []string{"mytopf.com", "top-fwz1.mail.ru"}

// RouterSites — сайты-SPA, чей клиентский роутер ждёт маршруты от корня (без basename): им скрываем префикс /proxy/<id>
// из location.pathname. Для остальных не трогаем: относительные URL в статической вёрстке разрешаются от адреса страницы.
var RouterSites = []string{"memealerts.com"}

func isRouterSite(site string) bool {
	for _, s := range RouterSites {
		if s == site {
			return true
		}
	}
	return false
}

// injection — тег, вставляемый в начало <head> проксируемой страницы.
// meta referrer=same-origin: браузер шлёт Referer на наш сервер, по нему определяется upstream для root-relative запросов.
func injection(id, origin string) string {
	u, _ := url.Parse(origin)
	host := ""
	if u != nil {
		host = u.Host
	}
	wsScheme := "ws"
	if strings.HasPrefix(origin, "https:") {
		wsScheme = "wss"
	}
	hostname := ""
	if u != nil {
		hostname = u.Hostname()
	}
	site := SiteOf(strings.ToLower(hostname))
	cfg, _ := json.Marshal(map[string]any{
		"id": id, "origin": origin, "host": host, "site": site, "wsScheme": wsScheme,
		"block": BlockedHosts, "spa": isRouterSite(site),
	})
	// "<" в JSON экранируем, чтобы строка не могла закрыть тег script
	safe := strings.ReplaceAll(string(cfg), "<", `<`)
	return `<meta name="referrer" content="same-origin"><script>` + clientPatchJS + "(" + safe + `)</script>`
}
