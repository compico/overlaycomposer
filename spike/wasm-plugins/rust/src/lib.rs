//! Rust-версия плагина hello (PoC серверных wasm-плагинов OC). Тот же ABI, что у Go-версии:
//! экспорты alloc/dealloc/oc_init/oc_call, импорт oc.host_call; данные — JSON; i64 = ptr<<32 | len.
//!
//! Сборка (цель wasm32-wasip1, reactor):
//!     rustup target add wasm32-wasip1
//!     cargo build --release --target wasm32-wasip1
//!     # результат: target/wasm32-wasip1/release/hello_rs.wasm
//! Запуск в хосте: cd ../host && go run . ../rust/target/wasm32-wasip1/release/hello_rs.wasm

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::collections::HashMap;

#[link(wasm_import_module = "oc")]
extern "C" {
    fn host_call(op_ptr: u32, op_len: u32, arg_ptr: u32, arg_len: u32) -> u64;
}

// ---- ABI ----

/// Выделяет буфер в памяти плагина и «отпускает» его: им владеет хост, пока не вызовет dealloc.
/// Длину запоминаем в первых 4 байтах перед буфером, чтобы dealloc знал размер без отдельного аргумента.
#[no_mangle]
pub extern "C" fn alloc(n: u32) -> u32 {
    let total = n as usize + 4;
    let mut v: Vec<u8> = Vec::with_capacity(total);
    v.extend_from_slice(&(total as u32).to_le_bytes());
    v.resize(total, 0);
    let p = v.as_mut_ptr();
    std::mem::forget(v);
    p as u32 + 4
}

#[no_mangle]
pub extern "C" fn dealloc(ptr: u32) {
    if ptr < 4 {
        return;
    }
    unsafe {
        let base = (ptr - 4) as *mut u8;
        let mut len = [0u8; 4];
        std::ptr::copy_nonoverlapping(base, len.as_mut_ptr(), 4);
        let total = u32::from_le_bytes(len) as usize;
        drop(Vec::from_raw_parts(base, total, total));
    }
}

fn read(ptr: u32, len: u32) -> Vec<u8> {
    if len == 0 {
        return Vec::new();
    }
    unsafe { std::slice::from_raw_parts(ptr as *const u8, len as usize).to_vec() }
}

fn pack(b: &[u8]) -> u64 {
    if b.is_empty() {
        return 0;
    }
    let p = alloc(b.len() as u32);
    unsafe { std::ptr::copy_nonoverlapping(b.as_ptr(), p as *mut u8, b.len()) };
    ((p as u64) << 32) | b.len() as u64
}

fn take(v: u64) -> Vec<u8> {
    if v == 0 {
        return Vec::new();
    }
    let (p, n) = ((v >> 32) as u32, v as u32);
    let out = read(p, n);
    dealloc(p);
    out
}

/// Вызов операции хоста: имя и JSON-аргумент → JSON-ответ.
fn host(op: &str, arg: Value) -> Value {
    let a = serde_json::to_vec(&arg).unwrap_or_default();
    let (op_p, ap) = (pack_in(op.as_bytes()), pack_in(&a));
    let r = unsafe { host_call(op_p, op.len() as u32, ap, a.len() as u32) };
    dealloc(op_p);
    dealloc(ap);
    serde_json::from_slice(&take(r)).unwrap_or(Value::Null)
}

fn pack_in(b: &[u8]) -> u32 {
    let p = alloc(b.len() as u32);
    unsafe { std::ptr::copy_nonoverlapping(b.as_ptr(), p as *mut u8, b.len()) };
    p
}

fn log(level: &str, msg: &str, fields: Value) {
    host("log", json!({ "level": level, "msg": msg, "fields": fields }));
}

fn get_int(key: &str) -> i64 {
    host("storage.get", json!({ "key": key }))["value"].as_str().and_then(|s| s.parse().ok()).unwrap_or(0)
}

// ---- плагин ----

#[derive(Deserialize, Default)]
struct Request {
    #[serde(default)]
    query: HashMap<String, String>,
}

#[derive(Serialize)]
struct Response {
    status: u16,
    body: Value,
}

fn respond(status: u16, body: Value) -> u64 {
    pack(&serde_json::to_vec(&Response { status, body }).unwrap())
}

#[no_mangle]
pub extern "C" fn oc_init(ptr: u32, len: u32) -> u64 {
    let _ = read(ptr, len); // {"apiVersion":1,"config":{...}}
    // Паника в Rust = trap без текста (panic=abort, нет stderr у wasm32-unknown-unknown): отправляем текст в лог хоста.
    std::panic::set_hook(Box::new(|info| log("error", "panic", json!({ "info": info.to_string() }))));
    log("info", "hello-rs plugin initialised", Value::Null);
    host("timer.set", json!({ "id": "t", "ms": 300, "repeat": true }));
    let meta = json!({
        "id": "hello-rs", "version": "0.1.0", "apiVersion": 1,
        "routes": [
            { "method": "GET", "path": "/greet", "handler": "greet" },
            { "method": "GET", "path": "/boom", "handler": "boom" },
            { "method": "GET", "path": "/ticks", "handler": "ticks" }
        ]
    });
    pack(&serde_json::to_vec(&meta).unwrap())
}

#[no_mangle]
pub extern "C" fn oc_call(mp: u32, ml: u32, ap: u32, al: u32) -> u64 {
    let name = String::from_utf8_lossy(&read(mp, ml)).to_string();
    let arg = read(ap, al);
    if name == "on_event" {
        if let Ok(ev) = serde_json::from_slice::<Value>(&arg) {
            if ev["type"] == "timer" {
                host("storage.set", json!({ "key": "ticks", "value": (get_int("ticks") + 1).to_string() }));
            }
        }
        return 0;
    }
    match name.as_str() {
        "greet" => {
            let req: Request = serde_json::from_slice(&arg).unwrap_or_default();
            let who = req.query.get("name").cloned().unwrap_or_else(|| "world".into());
            let n = get_int("greeted") + 1;
            host("storage.set", json!({ "key": "greeted", "value": n.to_string() }));
            log("info", "greet", json!({ "name": who, "count": n }));
            respond(200, json!({ "hello": who, "count": n }))
        }
        "ticks" => respond(200, json!({ "ticks": get_int("ticks") })),
        "boom" => {
            log("warn", "about to panic", Value::Null);
            let v: Vec<i32> = Vec::new();
            let i = v.len() + 1;
            respond(200, json!({ "x": v[i] })) // выход за границы → паника → trap
        }
        _ => respond(404, json!({ "error": format!("unknown handler {name}") })),
    }
}
