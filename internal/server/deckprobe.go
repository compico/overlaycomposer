package server

import "github.com/gofiber/fiber/v3"

// /deck/probe — проба совместимости: открыть на старом устройстве (iPad/Safari 9) и сообщить, что из веба там работает.
// Статичная страница на ES5 без зависимостей. Ничего не меняет и ничего не отправляет, кроме пробного WebSocket к /ws этого же сервера.
func (s *Server) deckProbe(c fiber.Ctx) error {
	c.Set("Cache-Control", "no-store")
	c.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self' ws: wss:; base-uri 'none'")
	return s.htmlString(c, deckProbePage)
}

const deckProbePage = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Deck probe</title>
<style>
body{margin:0;padding:20px;background:#111;color:#eee;font-family:-apple-system,Helvetica,Arial,sans-serif;font-size:18px}
h1{font-size:22px;font-weight:400;margin:0 0 6px}
p{color:#999;font-size:15px;margin:4px 0 14px}
table{border-collapse:collapse;width:100%}
td{padding:7px 10px;border-bottom:1px solid #2a2a2a;vertical-align:top}
td:last-child{text-align:right;white-space:nowrap}
.y{color:#60a917}.n{color:#e51400}.i{color:#1ba1e2}
pre{background:#1c1c1c;padding:12px;font-size:14px;white-space:pre-wrap;word-wrap:break-word}
</style></head><body>
<h1>Deck: compatibility probe</h1>
<p>Open this page on the device and send the text below. Nothing is changed or sent anywhere except a test WebSocket to this server.</p>
<table id="t"></table>
<pre id="sum"></pre>
<script>
(function(){
var rows=[],t=document.getElementById("t"),sum=document.getElementById("sum");
function add(name,value,state){
 rows.push(name+": "+value);
 var tr=document.createElement("tr"),a=document.createElement("td"),b=document.createElement("td");
 a.appendChild(document.createTextNode(name));
 b.appendChild(document.createTextNode(value));b.className=state||"i";
 tr.appendChild(a);tr.appendChild(b);t.appendChild(tr);
 sum.textContent=rows.join("\n");
}
function yn(name,ok){add(name,ok?"yes":"no",ok?"y":"n");}
function css(prop,val){var e=document.createElement("div");e.style.cssText=prop+":"+val;return e.style.length>0;}
add("User agent",navigator.userAgent);
add("Screen / window / DPR",screen.width+"x"+screen.height+" / "+window.innerWidth+"x"+window.innerHeight+" / "+(window.devicePixelRatio||1));
yn("XMLHttpRequest",typeof XMLHttpRequest!=="undefined");
yn("XMLHttpRequest timeout",typeof XMLHttpRequest!=="undefined"&&"timeout" in new XMLHttpRequest());
yn("WebSocket",typeof WebSocket!=="undefined");
yn("JSON",typeof JSON!=="undefined");
yn("Promise",typeof Promise!=="undefined");
yn("fetch",typeof fetch!=="undefined");
yn("classList",!!document.documentElement.classList);
yn("querySelectorAll",!!document.querySelectorAll);
yn("localStorage",(function(){try{localStorage.setItem("p","1");localStorage.removeItem("p");return true;}catch(e){return false;}})());
yn("requestAnimationFrame",typeof requestAnimationFrame!=="undefined");
yn("Touch events",("ontouchstart" in window));
yn("CSS: flexbox",css("display","flex")||css("display","-webkit-flex")||css("display","-webkit-box"));
yn("CSS: grid",css("display","grid"));
yn("CSS: variables",!!(window.CSS&&CSS.supports&&CSS.supports("--a","0")));
yn("CSS: transform",css("transform","scale(1)")||css("-webkit-transform","scale(1)"));
yn("CSS: calc",css("width","calc(1px + 1px)")||css("width","-webkit-calc(1px + 1px)"));
yn("Wake Lock (expected: no)",!!(navigator.wakeLock));
if(typeof WebSocket!=="undefined"){
 var started=new Date().getTime(),ws;
 add("WebSocket to /ws","connecting...");
 try{
  ws=new WebSocket((location.protocol==="https:"?"wss://":"ws://")+location.host+"/ws");
  ws.onopen=function(){add("WebSocket open",(new Date().getTime()-started)+" ms","y");};
  ws.onmessage=function(e){add("WebSocket first message",String(e.data).slice(0,60),"y");ws.onmessage=null;setTimeout(function(){ws.close();},300);};
  ws.onerror=function(){add("WebSocket error","see close code","n");};
  ws.onclose=function(e){add("WebSocket closed","code "+e.code,e.code===1000||e.code===1005?"y":"n");};
 }catch(e){add("WebSocket","exception: "+e,"n");}
}
})();
</script></body></html>`
