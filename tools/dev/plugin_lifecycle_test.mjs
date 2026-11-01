import fs from 'node:fs';import vm from 'node:vm';import assert from 'node:assert/strict';import blank from 'ts-blank-space';
const calls=[],sources={fx:{id:'fx',kind:'mm',classUid:'com.fx',path:'C:/fx@1',enabled:true},inst:{id:'inst',kind:'vst3',path:'C:/synth.vst3',enabled:true}};
const window={UI:{getJSON:async url=>url.startsWith('/api/sound/plugin')?(url.includes('inst')?{id:'VST3|synth',format:'vst3'}:{id:'com.fx',format:'mm',path:'C:/fx.dll'}):({plugins:url.endsWith('/installed')?[{id:'com.fx',version:'1',dir:'C:/fx@1',libPath:'C:/fx.dll'}]:[{id:'VST3|synth',path:'C:/synth.vst3'}]}),postJSON:async(url,body)=>{calls.push({url,body});if(url.endsWith('/state/load'))throw Object.assign(new Error('none'),{status:404});return {}},toast:()=>{}},SoundRegistry:{load:async()=>[],get:id=>sources[id],diskPathOf:s=>s?.path},EngineBridge:{setMaster:async gain=>calls.push({url:'master',gain}),setFxChain:async(track,slots)=>calls.push({url:'chain',track,slots})},AudioBackend:{isEngine:()=>true}};
vm.runInNewContext(blank(fs.readFileSync('frontend/src/arrangement/plugin_panel.ts','utf8')),{window,console,document:{},setTimeout,clearTimeout});
const ctrl={projectId:'P',projectEpoch:1,tracks:[{id:'T',source:{itemId:'I'}}],rack:{items:[{id:'I',kind:'vst3',sourceId:'inst'}]},mixer:{channels:{T:{fx:[]}},master:{gain:0,fx:[{slot:0,sourceId:'fx',bypass:true,wet:.2}]}}};
await window.PluginPanel.restoreAll(ctrl);assert.equal(ctrl.audioSync,'ready');
const loads=calls.filter(c=>c.url.endsWith('/plugins/load'));assert.equal(loads.length,2);assert.equal(loads[0].body.trackId,'T');assert.equal(loads[1].body.master,true);assert.equal(loads[1].body.format,'mm');
assert.equal(calls.find(c=>c.url==='master').gain,0);const chain=calls.find(c=>c.url==='chain');assert.equal(chain.slots[0].wet,.2);assert.equal(chain.slots[0].bypass,true);
assert.ok(calls.findIndex(c=>c.url==='chain')<calls.findIndex(c=>c.url.endsWith('/state/load')));
ctrl.projectId='empty';ctrl.projectEpoch++;ctrl.tracks=[];ctrl.mixer={channels:{},master:{gain:1,fx:[]}};await window.PluginPanel.restoreAll(ctrl);
assert.equal(calls.filter(c=>c.url.endsWith('/session')).length,2);assert.equal(ctrl.audioSync,'ready');assert.ok(calls.some(c=>c.url.endsWith('/state/save')&&c.body.project==='P'));
ctrl.projectId='reorder';ctrl.projectEpoch++;ctrl.tracks=[{id:'A',source:{itemId:'I'}},{id:'B',source:{itemId:'I'}}];ctrl.mixer={channels:{A:{fx:[]},B:{fx:[]}},master:{gain:.3,fx:[]}};
await window.PluginPanel.restoreAll(ctrl);calls.length=0;ctrl.tracks.reverse();await window.PluginPanel.restoreAll(ctrl);
assert.deepEqual(calls.filter(c=>c.url.endsWith('/plugins/load')).map(c=>c.body.trackId),['B','A']);
assert.deepEqual(Array.from(calls.find(c=>c.url.endsWith('/session')).body.tracks),['B','A']);
ctrl.tracks.pop();calls.length=0;await window.PluginPanel.restoreAll(ctrl);assert.equal(calls.filter(c=>c.url.endsWith('/plugins/load')).length,1);
calls.length=0;ctrl.mixer.master.gain=0;await window.PluginPanel.restoreAll(ctrl);assert.equal(calls.filter(c=>c.url.endsWith('/plugins/load')).length,0);assert.equal(calls.find(c=>c.url==='master').gain,0);
const originalPost=window.UI.postJSON;const warnings=[];window.UI.toast=message=>warnings.push(message);
window.UI.postJSON=async(url,body)=>{if(url.endsWith('/state/save'))throw new Error('backup blocked');return originalPost(url,body)};
calls.length=0;ctrl.projectId='blocked';ctrl.projectEpoch++;await window.PluginPanel.restoreAll(ctrl);
assert.equal(ctrl.audioSync,'failed');assert.ok(warnings.some(w=>w.includes('backup blocked')));assert.equal(calls.filter(c=>c.url.endsWith('/session')).length,0);
window.UI.postJSON=originalPost;
let release;const paused=new Promise(resolve=>release=resolve);let reached=false;
window.UI.postJSON=async(url,body)=>{const result=await originalPost(url,body);if(url.endsWith('/plugins/load')&&body.project==='slow'){reached=true;await paused;}return result;};
ctrl.projectId='slow';ctrl.projectEpoch++;ctrl.tracks=[{id:'A',source:{itemId:'I'}},{id:'B',source:{itemId:'I'}}];calls.length=0;const stale=window.PluginPanel.restoreAll(ctrl);
for(let n=0;n<50&&!reached;n++)await new Promise(r=>setTimeout(r,2));assert.ok(reached);
ctrl.projectId='current';ctrl.projectEpoch++;ctrl.tracks=[];const latest=window.PluginPanel.restoreAll(ctrl);release();await Promise.all([stale,latest]);
assert.equal(ctrl.audioSync,'ready');assert.equal(calls.filter(c=>c.url.endsWith('/plugins/load')&&c.body.project==='slow').length,1);
window.UI.postJSON=async(url,body)=>{if(url.endsWith('/state/load'))throw Object.assign(new Error('invalid plugin state'),{status:400});return originalPost(url,body);};
ctrl.projectId='bad-state';ctrl.projectEpoch++;ctrl.tracks=[{id:'A',source:{itemId:'I'}}];await window.PluginPanel.restoreAll(ctrl);assert.equal(ctrl.audioSync,'failed');assert.ok(warnings.some(w=>w.includes('invalid plugin state')));
window.UI.postJSON=originalPost;
ctrl.projectId='save-all';ctrl.projectEpoch++;ctrl.tracks=[{id:'A',source:{itemId:'I'}}];calls.length=0;
await window.PluginPanel.restoreAll(ctrl);calls.length=0;
await window.PluginPanel.saveAll(ctrl);
assert.ok(calls.some(c=>c.url.endsWith('/state/save')&&c.body.project==='save-all'&&c.body.trackId==='A'));

/* ── 恢复失败防护：第二次 load 失败 → session.signature 停在 "" ── */
window.UI.postJSON=async(url,body)=>{if(url.endsWith('/plugins/load')&&body.trackId==='B')throw new Error('second load boom');return originalPost(url,body)};
ctrl.projectId='half-load';ctrl.projectEpoch++;ctrl.tracks=[{id:'A',source:{itemId:'I'}},{id:'B',source:{itemId:'I'}}];ctrl.mixer={channels:{A:{fx:[]},B:{fx:[]}},master:{gain:1,fx:[]}};
await window.PluginPanel.restoreAll(ctrl);assert.equal(ctrl.audioSync,'failed');
window.UI.postJSON=originalPost;calls.length=0;
const saveWarns=[];window.UI.toast=m=>saveWarns.push(m);
/* signature==="" 时 saveAll 拒绝保存（快照可能是引擎默认值）并提示一次 */
await window.PluginPanel.saveAll(ctrl);
assert.equal(calls.filter(c=>c.url.endsWith('/state/save')).length,0);
assert.ok(saveWarns.some(w=>w.includes('插件状态未保存：上次音频恢复未完成')));
/* 下一次 fresh 恢复：旧会话 signature==="" → 整个备份循环跳过（不得把默认状态写回 .state） */
ctrl.projectId='after-fail';ctrl.projectEpoch++;
await window.PluginPanel.restoreAll(ctrl);assert.equal(ctrl.audioSync,'ready');
const preSession=calls.slice(0,Math.max(0,calls.findIndex(c=>c.url.endsWith('/session'))));
assert.equal(preSession.filter(c=>c.url.endsWith('/state/save')).length,0);

/* ── 参数批量下发 + 关窗兜底（独立上下文 + 最小 DOM 桩） ── */
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
function fakeNode(tag){const n={tagName:(tag||'div').toUpperCase(),className:'',children:[],style:{},dataset:{},value:'',type:'',title:'',textContent:'',tabIndex:0,offsetWidth:0,parentNode:null,handlers:{},classList:{add(){},remove(){}},setAttribute(){},appendChild(c){n.children.push(c)},addEventListener(t,f){(n.handlers[t]=n.handlers[t]||[]).push(f)},removeEventListener(){},querySelector(){return fakeNode('div')},_fire(t,ev){(n.handlers[t]||[]).forEach(f=>f(ev))}};return n;}
const created=[],calls2=[];
const doc2={createElement:tag=>{const n=fakeNode(tag);created.push(n);return n},body:fakeNode('body'),addEventListener(){},removeEventListener(){}};
const win2={UI:{getJSON:async url=>url.includes('/params')?{params:[{index:0,name:'Gain',value:.5},{index:1,name:'Tone',value:.25}],format:'vst3',name:'Synth'}:{},postJSON:async(url,body)=>{calls2.push({url,body});return{params:[]}},toast:()=>{}},SoundRegistry:{load:async()=>[],get:()=>null},AudioBackend:{isEngine:()=>true},setInterval,clearInterval,setTimeout,clearTimeout,innerWidth:1280};
vm.runInNewContext(blank(fs.readFileSync('frontend/src/arrangement/plugin_panel.ts','utf8')),{window:win2,console,document:doc2,setTimeout,clearTimeout});
await win2.PluginPanel.open({projectId:'PRJ',projectEpoch:3,tracks:[{id:'A'}]},{track:0,slot:-1,ref:'inst',name:'Synth'});
for(let n=0;n<50&&!created.filter(x=>x.className==='plg-param-slider').length;n++)await sleep(2);
const sliders=created.filter(x=>x.className==='plg-param-slider');assert.equal(sliders.length,2);
/* 快速连动两个滑杆 → 一次连接整批下发（不再 N×33ms 排队） */
sliders[0].value='0.7';sliders[0]._fire('input',{});
sliders[1].value='0.9';sliders[1]._fire('input',{});
await sleep(60);
let paramCalls=calls2.filter(c=>c.url.endsWith('/param'));
assert.equal(paramCalls.length,1);
assert.deepEqual(JSON.parse(JSON.stringify(paramCalls[0].body.changes)),[{index:0,value:0.7},{index:1,value:0.9}]);
assert.equal(paramCalls[0].body.project,'PRJ');assert.equal(paramCalls[0].body.generation,3);assert.equal(paramCalls[0].body.trackId,'A');
/* 关窗兜底：剩余 pending 立即 fire-and-forget 发出，不丢改动 */
sliders[0].value='0.1';sliders[0]._fire('input',{});
win2.PluginPanel.close();
await sleep(20);
paramCalls=calls2.filter(c=>c.url.endsWith('/param'));
assert.equal(paramCalls.length,2);
assert.deepEqual(JSON.parse(JSON.stringify(paramCalls[1].body.changes)),[{index:0,value:0.1}]);
assert.equal(win2.PluginPanel.isOpen(),false);
console.log('plugin lifecycle: references, stable reorder/delete, empty switch, serial generations, mix-only updates, backup failures, state error handling, saveAll, restore-failure guards and batched param flush passed');
