const channel=window.chrome.webview,token=window.name;let sequence=0;const pending=new Map();
function call(method,params={}){return new Promise((resolve,reject)=>{const requestId=`headless-${++sequence}`;pending.set(requestId,{resolve,reject});channel.postMessage({contract:"vibetable.plugin-api.v1",surfaceToken:token,requestId,method,params});});}
channel.addEventListener("message",e=>{const reply=e.data,saved=pending.get(reply?.requestId);if(!saved)return;pending.delete(reply.requestId);if(reply.error)saved.reject(new Error(reply.error.code));else saved.resolve(reply.result);});
function finish(status,summary){parent.postMessage({contract:"vibetable.plugin-surface.v1",surfaceToken:token,event:"close",payload:{status,summary}},"https://app.vibetable.local");}
await call("storage.set",{key:"headless-wait",value:true});setInterval(()=>{},100);
