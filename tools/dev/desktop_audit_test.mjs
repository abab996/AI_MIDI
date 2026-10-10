import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {CDP} from './browser_cdp.mjs';
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
let target;
for(let n=0;n<250;n++){
 try{const targets=await(await fetch('http://127.0.0.1:9237/json/list')).json();target=targets.find(t=>t.type==='page'&&t.url.includes('wails.localhost'));if(target)break;}catch{}
 await sleep(120);
}
assert.ok(target,'WebView2 debugging target unavailable');
const ws=new WebSocket(target.webSocketDebuggerUrl);await new Promise((resolve,reject)=>{ws.addEventListener('open',resolve,{once:true});ws.addEventListener('error',reject,{once:true})});
const cdp=new CDP(ws);await cdp.send('Runtime.enable');await cdp.send('Page.enable');await cdp.send('Log.enable');
try{
 await cdp.waitFor('window.UI && window.Arrange && window.EngineBridge && window.go?.app?.App');
 assert.equal(await cdp.evaluate('UI.getJSON("/api/settings").then(()=>true)'),true);
 await cdp.waitFor('UI.getJSON("/api/audio/status").then(s=>s.state==="ready")',30000);
 console.log('PASS desktop trusted bootstrap, Wails bridge and native engine ready');await cdp.evaluate('window.__auditToasts=[];const toast=UI.toast;UI.toast=function(...args){__auditToasts.push(args);return toast.apply(UI,args)};true');
 const result=await cdp.evaluate(`(async()=>{await UI.postJSON('/api/engine/plugins/sync',{});const sources=(await UI.getJSON('/api/sound/sources')).sources;return {synth:sources.find(s=>s.kind==='mm'&&s.classUid==='com.melodymate.example.synth'),gain:sources.find(s=>s.kind==='mm'&&s.classUid==='com.melodymate.example.gain')};})()`);
 assert.ok(result.synth&&result.gain,JSON.stringify(result));
 if(process.argv.includes('--reopen')){
  const proof=JSON.parse(fs.readFileSync(path.resolve('output/audit-2026-10-01/desktop/last-audit.json'),'utf8'));
  await cdp.evaluate(`Arrange.setProject(${JSON.stringify(proof.project)});true`);await cdp.waitFor("Arrange.audioSync==='ready'&&Arrange.loadedProjectId===Arrange.projectId");
  const restored=await cdp.evaluate(`UI.getJSON('/api/engine/plugins/params?'+new URLSearchParams({project:Arrange.projectId,generation:String(Arrange.projectEpoch),trackId:'stable-a'})).then(s=>s.params[0].value)`);assert.ok(Math.abs(restored-.2)<.001);
  assert.equal(await cdp.evaluate('Arrange.mixer.master.gain'),0);assert.equal(await cdp.evaluate('Arrange.mixer.master.fx[0].wet'),.25);assert.equal(await cdp.evaluate('Arrange.mixer.master.fx[0].bypass'),true);
  const state=await cdp.evaluate('UI.getJSON("/api/engine/plugins/state")');assert.equal(state.instruments.length,1);assert.equal(state.effects.length,1);
  const registry=await cdp.evaluate('UI.getJSON("/api/sound/sources")'),settings=await cdp.evaluate('UI.getJSON("/api/settings")');
  assert.equal(registry.sources.find(s=>s.id===proof.disabled).enabled,false);
  assert.ok(!registry.sources.some(s=>(s.dirs||[]).some(d=>path.resolve(d)===proof.deletedDir)));
  assert.ok(!(settings.material_dirs||[]).some(d=>path.resolve(d)===proof.deletedDir));
  console.log('PASS desktop restart keeps MM disabled and deleted sample pack out of registry and mirror');
  console.log('PASS desktop application reopen restores instances, stable states, gain 0 and FX parameters');
 }else{
 const project=await cdp.evaluate('UI.postJSON("/api/projects",{name:"Desktop native audit"})');
 const pid=project.id||project.meta?.id;assert.ok(pid);
 await cdp.evaluate(`Arrange.setProject(${JSON.stringify(pid)});true`);await cdp.waitFor(`Arrange.loadedProjectId===${JSON.stringify(pid)}&&Arrange.audioSync==='ready'`);
 await cdp.evaluate(`(async()=>{const arrangement=Arrange.serialize(),a=arrangement.tracks[0];a.id='stable-a';a.source={itemId:'instrument'};const b=JSON.parse(JSON.stringify(a));b.id='stable-b';b.name='B';arrangement.tracks=[a,b];arrangement.rack={version:2,items:[{id:'instrument',kind:'mm',sourceId:${JSON.stringify(result.synth.id)},name:'MM Synth'}]};arrangement.mixer={channels:{'stable-a':{fx:[]},'stable-b':{fx:[]}},master:{gain:0,fx:[{slot:0,sourceId:${JSON.stringify(result.gain.id)},bypass:true,wet:.25}]}};Arrange.deserialize(arrangement);await PluginPanel.restoreAll(Arrange);Arrange.dirty=true;await Arrange.doSave();})()`);
 await cdp.waitFor(`Arrange.audioSync==='ready'`);
 let state=await cdp.evaluate('UI.getJSON("/api/engine/plugins/state")');
 console.log('desktop native state',JSON.stringify(state));
 assert.equal(state.instruments.length,2);assert.equal(state.effects.length,1);
 await cdp.evaluate(`window.__auditPopup=UI.openPluginUI('/mm-ui.html?'+new URLSearchParams({plugin:${JSON.stringify(result.gain.id)},project:${JSON.stringify(pid)},generation:String(Arrange.projectEpoch),master:'1',slot:'0'}),'audit-native-ui');true`);
 await cdp.waitFor('document.querySelector(".plugin-ui-host iframe")?.contentDocument.getElementById("mmStatus")?.textContent.includes("就绪")',15000,'native MM custom UI ready');
 const resource=await cdp.evaluate('document.querySelector(".plugin-ui-host iframe").contentDocument.getElementById("plug").src');
 const frames=await cdp.send('Page.getFrameTree');
 const outer=frames.frameTree.childFrames.find(f=>f.frame.url.includes('/mm-ui.html'));
 const targets=await cdp.send('Target.getTargets'),sandbox=targets.targetInfos.find(t=>t.type==='iframe'&&t.url===resource);
 const changeGain='document.getElementById("gain").value="0.7";document.getElementById("gain").dispatchEvent(new Event("input",{bubbles:true}));true';
 if(sandbox){const session=(await cdp.send('Target.attachToTarget',{targetId:sandbox.targetId,flatten:true})).sessionId;await cdp.send('Runtime.evaluate',{expression:changeGain,returnByValue:true},session);await cdp.send('Target.detachFromTarget',{sessionId:session});}
 else{const child=outer.childFrames.find(f=>f.frame.url===resource),world=await cdp.send('Page.createIsolatedWorld',{frameId:child.frame.id,worldName:'audit'});const changed=await cdp.send('Runtime.evaluate',{expression:changeGain,contextId:world.executionContextId,returnByValue:true});assert.equal(changed.result.value,true);}
 await cdp.waitFor('UI.getJSON("/api/engine/plugins/params?"+new URLSearchParams({project:Arrange.projectId,generation:String(Arrange.projectEpoch),master:"1",slot:"0"})).then(s=>Math.abs(s.params[0].value-.7)<.001)');
 await cdp.screenshot('desktop-plugin-ui');
 await cdp.evaluate('document.querySelector(".plugin-ui-close").click();true');await cdp.waitFor('__auditPopup.closed');
 await cdp.waitFor(`fetch(${JSON.stringify(resource)}).then(r=>r.status===401)`);
 console.log('PASS desktop authenticated MM custom UI, SDK parameter write and session revocation');
 await cdp.evaluate(`UI.postJSON('/api/engine/plugins/param',{project:${JSON.stringify(pid)},generation:Arrange.projectEpoch,trackId:'stable-a',index:0,value:.2})`);
 await cdp.evaluate(`UI.postJSON('/api/engine/plugins/param',{project:${JSON.stringify(pid)},generation:Arrange.projectEpoch,trackId:'stable-b',index:0,value:.8})`);
 await cdp.evaluate(`Arrange.tracks.reverse();Arrange.dirty=true;Arrange.doSave().then(()=>PluginPanel.restoreAll(Arrange))`);assert.equal(await cdp.evaluate('Arrange.audioSync'),'ready');
 async function value(trackId){return cdp.evaluate(`UI.getJSON('/api/engine/plugins/params?'+new URLSearchParams({project:${JSON.stringify(pid)},generation:String(Arrange.projectEpoch),trackId:${JSON.stringify(trackId)}})).then(s=>s.params[0].value)`)}
 assert.ok(Math.abs(await value('stable-a')-.2)<.001);assert.ok(Math.abs(await value('stable-b')-.8)<.001);
 console.log('PASS desktop stable track reorder preserves distinct plugin states');
 await cdp.evaluate(`Arrange.tracks=Arrange.tracks.filter(t=>t.id!=='stable-b');Arrange.dirty=true;Arrange.doSave().then(()=>PluginPanel.restoreAll(Arrange))`);
 state=await cdp.evaluate('UI.getJSON("/api/engine/plugins/state")');assert.equal(state.instruments.length,1);assert.ok(Math.abs(await value('stable-a')-.2)<.001);
 console.log('PASS desktop delete clears retired instance without losing remaining state');
 // Exercise the real scheduler and the independently visible mixer.
 await cdp.evaluate('Arrange.open();ArrMixer.open(Arrange);Arrange.tracks[0].clips=[{id:"play-proof",type:"midi",start:0,length:4,notes:[{note:"C4",start:0,end:2,velocity:100}]}];Arrange.tracks.forEach(t=>Arrange.engine.ensureTrack(t));true');
 await cdp.waitFor('Arrange.tracks.every(t=>Arrange.engine.trackNodes[t.id]?._useNative)');
 await cdp.evaluate('Arrange.audioSync="waiting";Arrange.startPlayback();true');assert.equal(await cdp.evaluate('Arrange.isPlaying'),false);
 await cdp.evaluate('Arrange.audioSync="ready";Arrange.startPlayback();true');await cdp.waitFor('EngineBridge.getLevels().then(v=>v[0]>0.0001)',10000,'plugin track actual samples');await cdp.evaluate('Arrange.pausePlayback();Arrange.tracks[0].clips=[];true');
 const ops=await cdp.evaluate(`UI.postJSON('/api/projects/'+Arrange.projectId+'/arrangement/ops',{ops:[{op:'setMaster',gain:.2}]})`);
 await cdp.evaluate(`Arrange.close();Arrange.applyRemoteArrangement(${JSON.stringify(ops.arrangement)},['mixer']);PluginPanel.restoreAll(Arrange)`);
 assert.equal(await cdp.evaluate('document.querySelector("#mxrMaster .mxr-value").textContent'),await cdp.evaluate('ArrMixer.gainToDbText(.2)'));
 const zero=await cdp.evaluate(`UI.postJSON('/api/projects/'+Arrange.projectId+'/arrangement/ops',{ops:[{op:'setMaster',gain:0}]})`);await cdp.evaluate(`Arrange.applyRemoteArrangement(${JSON.stringify(zero.arrangement)},['mixer']);PluginPanel.restoreAll(Arrange)`);
 console.log('PASS desktop real plugin playback, waiting gate and remote mixer controls');
 await cdp.screenshot('desktop-native');
 // Save a proof for a second app launch with the same isolated data directory.
 const isolated=path.resolve('output/audit-2026-10-01/desktop'),sampleDir=path.join(isolated,'audit-samples-'+Date.now());fs.mkdirSync(sampleDir,{recursive:true});
 const pack=await cdp.evaluate(`UI.postJSON('/api/sound/sources',{kind:'samplepack',name:'Deleted restart audit',dirs:[${JSON.stringify(sampleDir)}]})`);
 await cdp.evaluate(`UI.postJSON('/api/sound/migrate',{}).then(()=>UI.delJSON('/api/sound/sources/'+${JSON.stringify(pack.source.id)}))`);
 const disabledDir=path.join(isolated,'Library/plugins/com.audit.disabled@1');fs.mkdirSync(disabledDir,{recursive:true});
 fs.mkdirSync(path.join(disabledDir,'bin/win-x64'),{recursive:true});fs.copyFileSync(path.join(isolated,'Library/plugins/com.melodymate.example.gain@1.0.0/bin/win-x64/mm_example_gain.dll'),path.join(disabledDir,'bin/win-x64/mm_example_gain.dll'));
 fs.writeFileSync(path.join(disabledDir,'manifest.json'),JSON.stringify({id:'com.audit.disabled',name:'Disabled audit fixture',version:'1',kind:'effect',entry:'win-x64/mm_example_gain.dll'}));
 await cdp.evaluate(`UI.postJSON('/api/engine/plugins/sync',{}).then(()=>UI.putJSON('/api/sound/sources/com_audit_disabled@1',{enabled:false})).then(()=>UI.postJSON('/api/engine/plugins/sync',{}))`);
 fs.writeFileSync(path.join(isolated,'last-audit.json'),JSON.stringify({project:pid,synth:result.synth.id,gain:result.gain.id,disabled:'com_audit_disabled@1',deletedDir:sampleDir}));
 const nativeDir=path.resolve('output/audit-2026-10-01/desktop/Library/vst3');
 await cdp.evaluate(`UI.postJSON('/api/engine/plugins/scan',{dirs:[${JSON.stringify(nativeDir)}]})`);
 const vst=await cdp.evaluate(`SoundRegistry.load(true).then(list=>list.find(s=>s.kind==='vst3'&&s.category==='instrument'))`);assert.ok(vst,'scanned VST instrument absent from registry');
 const item=await cdp.evaluate(`Arrange.ensureRackItem(${JSON.stringify(vst.id)},'','VST instrument')`);assert.ok(item?.id);
 await cdp.evaluate(`Arrange.assignRackItem(${JSON.stringify(item)},0,'');true`);await cdp.waitFor(`Arrange.audioSync==='ready'&&Arrange.tracks[0].source.itemId===${JSON.stringify(item.id)}`);
 state=await cdp.evaluate('UI.getJSON("/api/engine/plugins/state")');assert.equal(state.instruments.length,1);assert.equal(state.instruments[0].format,'vst3');
 const originalItem=await cdp.evaluate('Arrange.rack.items.find(i=>i.id==="instrument")');await cdp.evaluate(`Arrange.assignRackItem(${JSON.stringify(originalItem)},0,'');true`);await cdp.waitFor(`Arrange.audioSync==='ready'&&Arrange.tracks[0].source.itemId==='instrument'`);assert.ok(Math.abs(await value('stable-a')-.2)<.001);
 await cdp.evaluate('Arrange.doSave()');console.log('PASS desktop VST scan, registry instrument assignment and MM/VST replacement state preservation');
 const empty=await cdp.evaluate('UI.postJSON("/api/projects",{name:"Desktop empty audit"})'),emptyId=empty.id||empty.meta?.id;
 await cdp.evaluate(`Arrange.setProject(${JSON.stringify(emptyId)});true`);await cdp.waitFor(`Arrange.loadedProjectId===${JSON.stringify(emptyId)}&&Arrange.audioSync==='ready'`);
 state=await cdp.evaluate('UI.getJSON("/api/engine/plugins/state")');assert.equal(state.instruments.length,0);assert.equal(state.effects.length,0);
 console.log('PASS desktop empty project clears instrument and master FX');
 }
 console.log('real desktop audit passed');await cdp.evaluate('setTimeout(()=>window.runtime.Quit(),100);true');
}catch(error){console.error(cdp.errors);try{console.error('desktop snapshot',await cdp.evaluate('({sync:Arrange.audioSync,toasts:window.__auditToasts,tracks:Arrange.tracks,rack:Arrange.rack,ui:document.querySelector(".plugin-ui-host iframe")?.contentDocument?.getElementById("mmStatus")?.textContent,src:document.querySelector(".plugin-ui-host iframe")?.contentDocument?.getElementById("plug")?.src})'));await cdp.screenshot('desktop-failure');}catch{}throw error;}finally{ws.close();}
