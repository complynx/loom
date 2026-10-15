"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const source = fs.readFileSync(require("node:path").join(__dirname, "operator.js"), "utf8");
const tick = () => new Promise(resolve => setImmediate(resolve));
function harness(search, fetch) {
  class Node { append() {} }
  const elements = new Map();
  const element = id => {
    if (!elements.has(id)) elements.set(id, {value:"", disabled:false, tBodies:[{replaceChildren(){}, append(){}}], addEventListener(name, callback){this[name] = callback;}});
    return elements.get(id);
  };
  const context = vm.createContext({document:{getElementById:element, createElement:()=>new Node()}, Node, window:{location:{search,assign(){}}}, history:{replaceState(){}}, URLSearchParams, AbortController, setTimeout, clearTimeout, setInterval, clearInterval, fetch});
  vm.runInContext(source, context);
  return {context, element};
}
async function disconnectDuringLogin() {
  let release;
  const pending = new Promise(resolve => release = resolve);
  const {context,element} = harness("?code=stub&state=stub", async path => path.endsWith("/config") ? {ok:true,json:async()=>({enabled:true})} : pending);
  await tick();
  element("disconnect").click();
  release({ok:true,json:async()=>({access_token:"must-not-connect",expires_in:300})});
  await tick();
  assert.equal(vm.runInContext("token",context), "");
  assert.equal(element("notice").textContent, "Disconnected.");
}
async function disconnectDuringConfiguration() {
  let release;
  let exchanges = 0;
  const pending = new Promise(resolve => release = resolve);
  const {context,element} = harness("?code=stub&state=stub", async path => {
    if (path.endsWith("/config")) return pending;
    exchanges++;
    throw new Error("Canceled callback must not exchange");
  });
  element("login").disabled = true;
  element("disconnect").click();
  release({ok:true,json:async()=>({enabled:true})});
  await tick();
  assert.equal(element("login").disabled, false);
  assert.equal(vm.runInContext("token",context), "");
  assert.equal(exchanges, 0);
  assert.equal(element("notice").textContent, "Disconnected.");
}
async function paginationAndFilter() {
  const paths = [];
  let release;
  const pending = new Promise(resolve => release = resolve);
  let holdOverview = true;
  const overview = {overview:{}, runtime:{sampled_at:"now",dependencies:{}}};
  const {context,element} = harness("", async path => {
    if (path.endsWith("/config")) return {ok:true,json:async()=>({enabled:true})};
    paths.push(path);
    if (path === "/v1/admin/diagnostics" && holdOverview) return pending;
    return {ok:true,json:async()=>path.includes("/runs?") ? {items:[],next_cursor:"next"} : overview,headers:{get(){}}};
  });
  await tick();
  vm.runInContext('token = "stub"', context);
  const refresh = vm.runInContext('refresh("page-two")',context);
  assert.equal(element("state").disabled,true);
  holdOverview = false;
  release({ok:true,json:async()=>overview,headers:{get(){}}});
  await refresh;
  assert.equal(element("state").disabled,false);
  await vm.runInContext("refresh()",context);
  assert.match(paths.at(-1), /after_id=page-two/);
  element("state").value = "failed";
  element("state").change();
  await tick();
  assert.match(paths.at(-1), /state=failed/);
  assert.doesNotMatch(paths.at(-1), /after_id=/);
  element("disconnect").click();
  assert.equal(element("state").disabled,false);
}
(async()=>{await disconnectDuringLogin();await disconnectDuringConfiguration();await paginationAndFilter();console.log("PASS: pending login/config disconnect, page-preserving refresh, filter locking and cursor reset.");})().catch(error=>{console.error(error);process.exitCode=1;});
