import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
/* 承载页脚本已外置（CSP 收紧）：从产物 mm_ui_page.js 切片提取资源会话逻辑 */
const source=fs.readFileSync('frontend/js/mm_ui_page.js','utf8');
const startMark=source.indexOf('var resourceSession');
const slice=source.slice(startMark,source.indexOf('if (!pluginId)',startMark));
const tick=()=>new Promise(resolve=>setTimeout(resolve,5));
const deferred=()=>{let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve};};
for(const phase of ['ticket','exchange']){
 const pending=deferred(),revoked=[],frame={src:'',hidden:true},window={addEventListener(){}};
 const context={window,frame,elSkeleton:{},elStatus:{},pluginId:'fixture',q:new URLSearchParams('project=P&generation=1'),location:{origin:'http://localhost'},encodeURIComponent,Number,JSON,
  loadParams:async()=>{},post(){},theme:'dark',fetch:async(url,options)=>{
   if(url.endsWith('/revoke')){revoked.push(JSON.parse(options.body).session);return {ok:true};}
   if(url.endsWith('/ui-ticket'))return phase==='ticket'?pending.promise:{ok:true,json:async()=>({ticket:'session'})};
   return pending.promise;
  }};
 vm.runInNewContext(slice+'\nthis.load=loadFrame;',context);context.load();await tick();window.MMPluginHostClose();
 pending.resolve({ok:true,json:async()=>phase==='ticket'?({ticket:'session'}):({session:'session',url:'/resources/session/editor.html'})});await tick();await tick();
 assert.equal(frame.src,'',`closed UI navigated after pending ${phase}`);assert.ok(revoked.includes('session'),`pending ${phase} was not revoked`);
}
console.log('MM UI lifecycle: close during pending ticket or exchange prevents navigation and revokes grant');
