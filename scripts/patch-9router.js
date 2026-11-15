const fs = require("fs");
const path = require("path");

// 1. Patch /v1/audio/transcriptions/route.js for undici.FormData + STT Combo support
const routePath = "/app/.next/server/app/api/v1/audio/transcriptions/route.js";
if (fs.existsSync(routePath)) {
  let c = fs.readFileSync(routePath, "utf8");
  c = c.replaceAll("new FormData", 'new(require("undici").FormData)');

  const target = 'let h=await (0,f.mA)(c);if(!h.provider)return(0,i.yj)(G.gx.BAD_REQUEST,"Invalid model format");';
  const comboSnippet =
    'let _cm=await (0,f.d_)(c);if(_cm&&_cm.length>0){let _st=(await (0,e.mt)()).comboStrategies?.[c]?.fallbackStrategy==="round-robin",_list=[..._cm];if(_st){globalThis.__sttRR=(globalThis.__sttRR||0)+1;let _idx=globalThis.__sttRR%_list.length;_list=[..._list.slice(_idx),..._list.slice(0,_idx)];}V.info("STT",`Combo "${c}" with ${_list.length} models`);let _last=null;for(let _m of _list){let _fd={get:k=>k==="model"?_m:b.get(k)};_last=await $({formData:async()=>_fd,headers:a.headers,signal:a.signal,url:a.url});if(_last&&_last.status>=200&&_last.status<300)return _last;}return _last||(0,i.yj)(G.gx.BAD_GATEWAY,"Combo STT failed");}' +
    target;

  // Replace previous combo snippet if present, or inject before target
  c = c.replace(/let _cm=await \(0,f\.d_\)\(c\);[\s\S]*?Combo STT failed"\);\}/, "");
  if (c.includes(target)) {
    c = c.replace(target, comboSnippet);
  }
  fs.writeFileSync(routePath, c);
}

// Helper to recursively find .js files in a directory
function findJsFiles(dir) {
  if (!fs.existsSync(dir)) return [];
  const results = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      results.push(...findJsFiles(full));
    } else if (entry.isFile() && entry.name.endsWith(".js")) {
      results.push(full);
    }
  }
  return results;
}

// 2. Enable "Create Combo" button & STT label across server + client chunks dynamically
const dirsToPatch = [
  "/app/.next/server/app/(dashboard)/dashboard/media-providers",
  "/app/.next/static/chunks/app/(dashboard)/dashboard/media-providers",
];
for (const dir of dirsToPatch) {
  for (const p of findJsFiles(dir)) {
    let c = fs.readFileSync(p, "utf8");
    let updated = c
      .replace(
        'new Set([]),m={image:"image-combo",tts:"tts-combo"}',
        'new Set(["stt","tts","image"]),m={image:"image-combo",tts:"tts-combo",stt:"stt-combo"}'
      )
      .replace(
        'new Set([]),x={image:"image-combo",tts:"tts-combo"}',
        'new Set(["stt","tts","image"]),x={image:"image-combo",tts:"tts-combo",stt:"stt-combo"}'
      )
      .replace(
        'tts:"Text To Speech"}',
        'tts:"Text To Speech",stt:"Speech to Text"}'
      );
    if (updated !== c) {
      fs.writeFileSync(p, updated);
    }
  }
}

console.log("[patch-9router] Applied STT FormData + STT Combo patches successfully.");
