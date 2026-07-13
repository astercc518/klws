#!/usr/bin/env node
// 扫源码里的 t("key") 字面量,核对每个 key 在合并字典 zh/en 两侧都有定义。
// 用正则而非 import:字典是 TS 模块,直接文本抽取 "key": 定义即可。
//
// 注:key 允许大小写混合(驼峰,如 "table.searchPlaceholder"),字符类须含 A-Z,
// 否则会漏检绝大多数既有键,校验形同虚设。
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

const SRC_DIRS = ["components", "app"];
const DICT_DIR = "lib/i18n/dicts";

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.(tsx|ts)$/.test(name)) out.push(p);
  }
  return out;
}

const used = new Set();
for (const dir of SRC_DIRS)
  for (const f of walk(dir))
    for (const m of readFileSync(f, "utf8").matchAll(/\bt\(\s*"([a-zA-Z0-9.]+)"\s*[),]/g))
      used.add(m[1]);

const defined = { zh: new Set(), en: new Set() };
for (const f of walk(DICT_DIR)) {
  const src = readFileSync(f, "utf8");
  // 每个文件里 zh 块在 en 块之前(与 common.ts 一致的约定)
  const zhPart = src.slice(src.indexOf("zh:"), src.indexOf("en:"));
  const enPart = src.slice(src.indexOf("en:"));
  for (const m of zhPart.matchAll(/"([a-zA-Z0-9.]+)":/g)) defined.zh.add(m[1]);
  for (const m of enPart.matchAll(/"([a-zA-Z0-9.]+)":/g)) defined.en.add(m[1]);
}

let bad = 0;
for (const k of [...used].sort()) {
  const missZh = !defined.zh.has(k), missEn = !defined.en.has(k);
  if (missZh || missEn) { bad++; console.error(`MISSING ${k}${missZh ? " [zh]" : ""}${missEn ? " [en]" : ""}`); }
}
console.log(`${used.size} keys used, ${bad} missing`);
process.exit(bad ? 1 : 0);
