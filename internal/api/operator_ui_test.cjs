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
  const destinations = [];
  const context = vm.createContext({document:{getElementById:element, createElement:()=>new Node()}, Node, window:{location:{search,assign(path){destinations.push(path);}}}, history:{replaceState(){}}, URLSearchParams, AbortController, setTimeout, clearTimeout, setInterval, clearInterval, fetch});
  vm.runInContext(source, context);
  return {context, element, destinations};
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
async function proxyLoginAndRevocation(redirect = false) {
  const paths = [];
  let denied = false;
  const {context,element,destinations} = harness("", async (path, options) => {
    if (path.endsWith("/config")) return {ok:true,json:async()=>({enabled:true,proxy:true})};
    paths.push(path);
    assert.match(path, /^\/admin\/api\/diagnostics/);
    assert.equal(options.credentials, "same-origin");
    assert.equal(options.redirect, "manual");
    assert.equal(options.headers.Authorization, undefined);
    if (denied) return redirect ? {ok:false,type:"opaqueredirect",status:0} : {ok:false,status:403};
    return {ok:true,json:async()=>path.includes("/runs?") ? {items:[]} : {overview:{},runtime:{dependencies:{}}}};
  });
  await tick();
  assert.equal(vm.runInContext("token", context), "");
  assert.equal(element("refresh").disabled, false);
  assert.equal(paths.length, 2);
  denied = true;
  await vm.runInContext("refresh()", context);
  assert.equal(element("refresh").disabled, true);
  assert.match(element("notice").textContent, redirect ? /SSO sign-in required/ : /Authorization denied/);
  element("login").click();
  assert.deepEqual(destinations, ["/admin"]);
  element("disconnect").click();
}
(async()=>{await disconnectDuringLogin();await disconnectDuringConfiguration();await paginationAndFilter();await proxyLoginAndRevocation();await proxyLoginAndRevocation(true);console.log("PASS: PKCE races, pagination, proxy cookie requests without tokens, revocation, expired-SSO redirects and reconnect.");})().catch(error=>{console.error(error);process.exitCode=1;});
