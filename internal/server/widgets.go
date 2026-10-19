package server

// Демо-виджеты для проверки без внешних сервисов.

const clockWidget = `<!doctype html><meta charset="utf-8">
<style>
  html,body{margin:0;height:100%;background:rgba(15,15,25,.85);color:#fff;font-family:system-ui,sans-serif;
    display:flex;align-items:center;justify-content:center;font-size:64px;font-weight:700;letter-spacing:2px}
</style>
<div id="t"></div>
<script>
  const t=document.getElementById('t');
  const tick=()=>t.textContent=new Date().toLocaleTimeString();
  tick();setInterval(tick,1000);
</script>`

const boxWidget = `<!doctype html><meta charset="utf-8">
<style>
  html,body{margin:0;height:100%;font-family:system-ui,sans-serif;color:#fff}
  body{background:linear-gradient(135deg,#7c3aed,#06b6d4);display:flex;align-items:center;justify-content:center;
    font-size:40px;font-weight:700}
</style>
<div>Demo widget</div>`
