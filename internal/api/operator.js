"use strict";
const element = id => document.getElementById(id);
let proxyMode = false, proxyConnected = false;
function connected() { return proxyConnected || token !== ""; }
let token = "", cursor = "", currentAfterID = "", busy = false, timer = null, expiry = null, generation = 0;
function notice(text, error = false) { element("notice").textContent = text; element("notice").className = error ? "error" : ""; }
function clearData() { for (const id of ["overview", "runs"]) element(id).tBodies[0].replaceChildren(); element("detail").hidden = true; element("more").disabled = true; cursor = ""; currentAfterID = ""; }
function disconnect() { clearFunding(); token = ""; proxyConnected = false; generation++; clearInterval(timer); timer = null; clearTimeout(expiry); expiry = null; element("refresh").disabled = true; element("state").disabled = false; clearData(); notice("Disconnected."); }
async function api(path, options = {}) {
  const expected = generation;
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 10000);
  try {
    const endpoint = proxyMode ? "/admin/api" + path.slice("/v1/admin".length) : path;
    const response = await fetch(endpoint, {...options, headers:{...(proxyMode ? {} : {Authorization:"Bearer " + token}), ...(options.body ? {"Content-Type":"application/json", "X-Loom-Admin-Request":"1"} : {})}, cache:"no-store", credentials:proxyMode ? "same-origin" : "omit", redirect:"manual", signal:controller.signal});
    if (expected !== generation) throw new Error("Session changed");
    if (response.type === "opaqueredirect" || (response.status >= 300 && response.status < 400)) { const message = "SSO sign-in required. Reconnect to continue."; disconnect(); notice(message, true); throw new Error(message); }
    if (response.status === 403 && options.funding) throw new Error("Funding permission denied; no change was applied.");
    if (response.status === 401 || response.status === 403) { const message = "Authorization denied. Sign in again with diagnostics.read."; disconnect(); notice(message, true); throw new Error(message); }
    if (!response.ok) throw new Error("Request failed: HTTP " + response.status + " · request " + (response.headers.get("X-Request-ID") || "unavailable"));
    const value = options.funding ? parseFundingJSON(await response.text()) : await response.json();
    if (expected !== generation) throw new Error("Session changed");
    return value;
  } finally { clearTimeout(timeout); }
}
function row(table, values) { const tr = document.createElement("tr"); for (const value of values) { const td = document.createElement("td"); if (value instanceof Node) td.append(value); else td.textContent = String(value ?? ""); tr.append(td); } table.append(tr); }
async function showDetail(id) {
  try { const detail = await api("/v1/admin/diagnostics/runs/" + encodeURIComponent(id)); element("detail-json").textContent = JSON.stringify(detail, null, 2); element("detail-notice").textContent = "Safe metadata only. Partial lists are marked in the response."; element("detail").hidden = false; }
  catch (error) { notice(error.message, true); }
}
async function loadRuns(after = "") {
  const query = new URLSearchParams({state:element("state").value, limit:"50"}); if (after) query.set("after_id", after);
  const page = await api("/v1/admin/diagnostics/runs?" + query);
  const tbody = element("runs").tBodies[0]; tbody.replaceChildren();
  for (const run of page.items) { const button = document.createElement("button"); button.textContent = run.id; button.addEventListener("click", () => showDetail(run.id)); row(tbody,[button,run.state,run.created_at,run.reserved_microcredits,run.spent_microcredits,[run.pause_requested ? "pause" : "", run.cancel_requested ? "cancel" : ""].filter(Boolean).join(", ")]); }
  cursor = page.next_cursor || ""; element("more").disabled = !cursor;
}
async function refresh(after = currentAfterID) {
  if (!connected() || busy) return;
  const expected = generation;
  busy = true; element("refresh").disabled = true; element("state").disabled = true;
  try {
    const snapshot = await api("/v1/admin/diagnostics"); const tbody = element("overview").tBodies[0]; tbody.replaceChildren();
    for (const [key,value] of Object.entries(snapshot.overview)) { if (typeof value === "object" && value !== null) { for (const [name,count] of Object.entries(value)) row(tbody,[key + "." + name,count]); } else row(tbody,[key,value]); }
    row(tbody,["runtime.sampled_at",snapshot.runtime.sampled_at]);
    for (const [name,up] of Object.entries(snapshot.runtime.dependencies || {})) row(tbody,[name,up ? "available" : "unavailable"]);
    await loadRuns(after); currentAfterID = after; notice("Updated " + new Date().toLocaleTimeString() + ". Refreshes every 15 seconds; no prompt or credential contents.");
  } catch (error) { if (expected === generation) notice(error.message, true); }
  finally { busy = false; element("refresh").disabled = !connected(); element("state").disabled = false; }
}
element("login").addEventListener("click", () => { window.location.assign(proxyMode ? "/admin" : "/admin/login"); });
async function initializeLogin() {
  const expected = generation;
  const callback = new URLSearchParams(window.location.search);
  const code = callback.get("code"), state = callback.get("state"), denied = callback.has("error");
  if (code || state || denied) history.replaceState(null, "", "/admin");
  try {
    const configuration = await fetch("/admin/login/config", {cache:"no-store", credentials:"same-origin"});
    if (!configuration.ok) throw new Error("Login configuration unavailable.");
    const settings = await configuration.json();
    element("login").disabled = !settings.enabled;
    if (expected !== generation) return;
    if (!settings.enabled) { notice("Zitadel login is not configured."); return; }
    if (settings.proxy) {
      proxyMode = true; proxyConnected = true; element("login").textContent = "Reconnect";
      await refresh(); if (proxyConnected) timer = setInterval(() => refresh(), 15000);
      return;
    }
    if (denied) throw new Error("Sign-in was denied. Try again.");
    if (!code && !state) { notice("Sign in to inspect diagnostics for your organization."); return; }
    if (!code || !state) throw new Error("Invalid sign-in callback. Try again.");
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 15000);
    let response;
    try { response = await fetch("/admin/login/exchange", {method:"POST", headers:{"Content-Type":"application/json"}, body:JSON.stringify({code,state}), credentials:"same-origin", cache:"no-store", signal:controller.signal}); }
    finally { clearTimeout(timeout); }
    if (!response.ok) throw new Error(response.status === 403 ? "Diagnostics permission required. Sign in with an authorized account." : "Sign-in failed or expired. Try again.");
    const credential = await response.json();
    if (expected !== generation) return;
    disconnect(); token = credential.access_token;
    expiry = setTimeout(() => { disconnect(); notice("Sign-in expired. Sign in again."); }, Math.min(credential.expires_in * 1000, 2147483647));
    await refresh(); if (token) timer = setInterval(() => refresh(), 15000);
  } catch (error) { if (expected !== generation) return; notice(error.name === "AbortError" ? "Sign-in timed out. Try again." : error.message, true); }
}
let fundingSettings = null, fundingOwner = "", fundingPolicyVersion = null, fundingDefaultsEpoch = 0, fundingOwnerEpoch = 0;
function clearFunding() {
 fundingDefaultsEpoch++; fundingOwnerEpoch++;
 fundingSettings=null; fundingOwner=""; fundingPolicyVersion=null;
 element("funding-save-defaults").disabled=true; element("funding-save-owner").disabled=true;
 element("funding-status").tBodies[0].replaceChildren();
}
// Preserve integer tokens that exceed JavaScript's exact range for status/receipt display.
function parseFundingJSON(text) {
 return JSON.parse(text.replace(/"(?:\\.|[^"\\])*"|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/g, token => {
  if(token.startsWith('"') || /[.eE]/.test(token) || Number.isSafeInteger(Number(token)))return token;
  return JSON.stringify(token);
 }));
}
function applyFundingDefaults(value) {
 if(!Number.isSafeInteger(value.version))throw new Error("Settings version exceeds browser precision; use the API.");
 fundingSettings=value;
 element("funding-enabled").checked=value.defaults.enabled;
 element("funding-base").value=value.defaults.base_microcredits;
 element("funding-factor").value=value.defaults.factor_ppm;
}
function applyFundingPolicy(value) {
 if(!Number.isSafeInteger(value.policy.version))throw new Error("Policy version exceeds browser precision; use the API.");
 fundingPolicyVersion=value.policy.version;
 element("funding-participation").value=value.policy.enabled===null ? "" : String(value.policy.enabled);
 element("funding-user-base").value=value.policy.base_microcredits ?? "";
 element("funding-user-factor").value=value.policy.factor_ppm ?? "";
 renderFunding(value);
}
function fundingNotice(text) { element("funding-notice").textContent=text; }
function fundingInteger(id, nullable=false) {
 const text=element(id).value.trim();
 if (nullable && text==="") return null;
 const number=Number(text);
 if (text==="" || !Number.isSafeInteger(number) || number<0) throw new Error("Enter nonnegative whole numbers within the browser's safe integer range.");
 return number;
}
function renderFunding(value) {
 const body=element("funding-status").tBodies[0]; body.replaceChildren();
 row(body,["Participation",value.enabled ? "Enabled" : "Disabled"]);
 row(body,["Owner type",value.classification]);
 row(body,["Current target (microcredits)",value.target_microcredits]);
 row(body,["Next refill (UTC)",value.next_refill_at || "None"]);
 const receipt=value.last_receipt;
 if (receipt) for (const [label,value] of [["Last period",receipt.period],["Recorded target",receipt.target_microcredits],["Balance used",receipt.balance_microcredits],["Credited",receipt.credited_microcredits],["Reason",receipt.reason],["Settings version",receipt.settings_version]]) row(body,[label,value]);
}
element("funding-load-defaults").addEventListener("click",async()=>{
 const epoch=++fundingDefaultsEpoch;
 fundingSettings=null;element("funding-save-defaults").disabled=true;
 try {
  const value=await api("/v1/admin/settings/monthly-funding",{funding:true});
  if(epoch!==fundingDefaultsEpoch)return;
  applyFundingDefaults(value);
  element("funding-save-defaults").disabled=false; fundingNotice("Defaults loaded. Save uses settings version "+value.version+".");
 } catch(error) {if(epoch===fundingDefaultsEpoch)fundingNotice(error.message);}
});
element("funding-defaults").addEventListener("submit",async event=>{
 event.preventDefault(); if(!fundingSettings)return;
 const epoch=++fundingDefaultsEpoch;
 element("funding-save-defaults").disabled=true;
 try {
  const monthly_funding={enabled:element("funding-enabled").checked,base_microcredits:fundingInteger("funding-base"),factor_ppm:fundingInteger("funding-factor")};
  const value=await api("/v1/admin/settings/monthly-funding",{funding:true,method:"PATCH",body:JSON.stringify({version:fundingSettings.version,defaults:monthly_funding})});
  if(epoch!==fundingDefaultsEpoch)return;
  applyFundingDefaults(value); fundingNotice("Defaults saved. Completed periods are unchanged.");
  element("funding-save-defaults").disabled=false;
 } catch(error) {if(epoch!==fundingDefaultsEpoch)return;fundingSettings=null;element("funding-save-defaults").disabled=true; fundingNotice(error.message+" Reload defaults before another save; verify state if the response was lost.");}
});
element("funding-load-owner").addEventListener("click",async()=>{
 const epoch=++fundingOwnerEpoch;
 element("funding-status").tBodies[0].replaceChildren();
 element("funding-save-owner").disabled=true; fundingPolicyVersion=null; fundingOwner="";
 try {
  const owner=element("funding-owner").value.trim();
  if(!owner)throw new Error("Enter an owner ID.");
  const value=await api("/v1/admin/billing/"+encodeURIComponent(owner)+"/monthly-funding",{funding:true});
  if(epoch!==fundingOwnerEpoch || element("funding-owner").value.trim()!==owner)return;
  fundingOwner=owner;applyFundingPolicy(value);element("funding-save-owner").disabled=false;fundingNotice("Owner policy loaded.");
 }catch(error){if(epoch===fundingOwnerEpoch)fundingNotice(error.message);}
});
element("funding-owner").addEventListener("input",()=>{fundingOwnerEpoch++;element("funding-status").tBodies[0].replaceChildren();fundingOwner="";fundingPolicyVersion=null;element("funding-save-owner").disabled=true;});
element("funding-policy").addEventListener("submit",async event=>{
 event.preventDefault();if(fundingPolicyVersion===null || !fundingOwner)return;
 const owner=fundingOwner, epoch=++fundingOwnerEpoch;
 element("funding-save-owner").disabled=true;
 try {
  const selected=element("funding-participation").value;
  const policy={version:fundingPolicyVersion,enabled:selected==="" ? null : selected==="true",base_microcredits:fundingInteger("funding-user-base",true),factor_ppm:fundingInteger("funding-user-factor",true)};
  const value=await api("/v1/admin/billing/"+encodeURIComponent(owner)+"/monthly-funding",{funding:true,method:"PATCH",body:JSON.stringify(policy)});
  if(epoch!==fundingOwnerEpoch || fundingOwner!==owner)return;
  applyFundingPolicy(value);element("funding-save-owner").disabled=false;
  fundingNotice("Owner policy saved. Completed periods are unchanged; use billing adjustments for immediate funding.");
 }catch(error){if(epoch!==fundingOwnerEpoch || fundingOwner!==owner)return;fundingPolicyVersion=null;element("funding-save-owner").disabled=true;fundingNotice(error.message+" Reload owner before another save; verify state if the response was lost.");}
});
initializeLogin();
element("disconnect").addEventListener("click", disconnect);
element("refresh").addEventListener("click", () => refresh());
element("state").addEventListener("change", () => { currentAfterID = ""; refresh(); });
element("more").addEventListener("click", () => refresh(cursor));
