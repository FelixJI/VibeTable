"use strict";
const channel = window.chrome.webview;
const token = window.name;
const pending = new Map();
let sequence = 0;
function call(method, params = {}, suppliedToken = token) {
  return new Promise((resolve, reject) => {
    const requestId = `application-${++sequence}`;
    const timer = setTimeout(() => { pending.delete(requestId); reject(new Error("reply deadline")); }, method === "mutation.submit" ? 125000 : 20000);
    pending.set(requestId, {resolve,reject,timer});
    channel.postMessage({contract:"vibetable.plugin-api.v1",surfaceToken:suppliedToken,requestId,method,params});
    if(method === "mutation.submit") window.lastSubmit = requestId;
  });
}
window.call = call;
channel.addEventListener("message", e => {
  const reply=e.data;
  if(reply?.contract!=="vibetable.plugin-api.v1")return;
  const saved=pending.get(reply.requestId);if(!saved)return;
  pending.delete(reply.requestId);clearTimeout(saved.timer);
  if(reply.error)saved.reject(Object.assign(new Error(reply.error.message),{code:reply.error.code}));else saved.resolve(reply.result);
});
window.addEventListener("message", e => {
  if(e.origin!=="https://app.vibetable.local"||e.source!==parent||e.data?.surfaceToken!==token)return;
  if(e.data.event==="themeChanged"){
    const theme=e.data.payload;for(const [key,value]of Object.entries(theme.variables))document.documentElement.style.setProperty(key,value);
    document.documentElement.style.colorScheme=theme.mode;document.documentElement.dataset.theme=theme.mode;
  }
});
function route(){const name=location.hash.slice(1)||"list";for(const id of ["list","detail","stats"])document.getElementById(id).hidden=id!==name;}
window.addEventListener("hashchange",route);route();
let context,description,row,field,guard;
async function read(){context=await call("context.read");description=await call("data.describe",{accepts:["vibetable.plugin-data.v2"],collection:context.collection});field=description.fields.find(f=>!f.readonly&&f.logicalType==="text").fieldId;
 const page=await call("data.query",{contract:"vibetable.plugin-query.v2",collection:context.collection,fields:["id",field],sorts:[{field:"id",direction:"asc"}],pageSize:200});row=page.items[0]||null;guard=row?page.rowGuards[String(row.id)]:null;document.getElementById("rows").textContent=JSON.stringify(page.items);document.getElementById("draft").value=row?row[field]:"";document.getElementById("status").textContent="ready";}
document.getElementById("save").addEventListener("click",async()=>{try{const preview=await call("mutation.preview",{contract:"vibetable.surface-mutation-plan.v1",collection:description.collection,schemaRevision:description.schemaRevision,operations:[row?{kind:"update",primaryKey:String(row.id),expectedDigest:guard,values:{[field]:document.getElementById("draft").value}}:{kind:"create",values:{[field]:document.getElementById("draft").value}}]});const result=await call("mutation.submit",{planId:preview.planId});document.getElementById("result").textContent=result.status;await read();}catch(error){document.getElementById("result").textContent=error.code||error.message;}});
document.getElementById("cancel").addEventListener("click",()=>call("request.cancel",{requestId:window.lastSubmit}));
document.getElementById("hang").addEventListener("click",()=>{document.getElementById("status").textContent="loop-entered";while(true){}});
read().catch(e=>document.getElementById("status").textContent=e.code||e.message);
