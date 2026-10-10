import fs from 'node:fs';
import vm from 'node:vm';
import assert from 'node:assert/strict';
import blank from 'ts-blank-space';
const noop=()=>{}, copy=x=>JSON.parse(JSON.stringify(x));
const tick=()=>new Promise(r=>setTimeout(r,10));
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b});return {promise,resolve,reject}};
function harness(get=async()=>({}),put=async()=>({rev:2})) {
  const drafts=new Map();
  const window={UI:{getJSON:get,putJSON:put,postJSON:async()=>({}),toast:noop},ArrangeEngine:class {},EngineBridge:{},ArrRack:{cloneItem:copy},addEventListener:noop};
  const ctx={window,document:{addEventListener:noop},console,setTimeout,clearTimeout};
  for(const file of ['sync','arrange'])vm.runInNewContext(blank(fs.readFileSync(`frontend/src/arrangement/${file}.ts`,'utf8')),ctx);
  window.ArrangementSync.draft=async(method,id,value)=>{if(method==='put')drafts.set(id,copy(value));if(method==='delete')drafts.delete(id);return drafts.get(id)};
  window.ArrangementSync.chooseConflict=async()=>null;
  const a=window.Arrange;
  for(const name of ['stopPlayback','setSaveStamp','syncTransportUI','renderAll','notifyRackChanged','normalizeRack'])a[name]=noop;
  a.deserialize=function(d){this.tracks=copy(d.tracks);this.rev=d.rev;this.mixer=copy(d.mixer||{});this.rack=copy(d.rack||{})};
  a.serialize=function(){return copy({rev:this.rev,tracks:this.tracks,mixer:this.mixer,rack:this.rack})};
  const initialize=()=>{a.projectId='A';a.loadedProjectId='A';a.tracks=[{id:'one',name:'original',clips:[]}];a.rev=1;a._saves.A={base:a.serialize(),inflight:null,draft:null}};
  return {a,window,drafts,initialize};
}
// The delayed load from A cannot change project B.
{
 const pending={}; const {a}=harness(url=>url.endsWith('/bpm')?Promise.resolve({bpm:120}):(pending[url]=deferred()).promise);
 a.setProject('A');a.setProject('B');await tick();
 pending['/api/projects/B/arrangement'].resolve({rev:2,tracks:[{id:'B',clips:[]}]});await tick();
 pending['/api/projects/A/arrangement'].resolve({rev:1,tracks:[{id:'A',clips:[]}]});await tick();
 assert.equal(a.tracks[0].id,'B');assert.equal(a.loadedProjectId,'B');
}
// A pending save is reused, and edits made during it use the next acknowledged rev.
{
 const requests=[]; const {a,initialize}=harness(undefined,(url,body)=>{const d=deferred();requests.push({url,body,d});return d.promise});initialize();
 a.tracks[0].name='first';a.dirty=true;const first=a.doSave();await tick();
 a.tracks[0].name='second';a.dirty=true;assert.equal(a.doSave(),first);await tick();assert.equal(requests.length,1);
 requests[0].d.resolve({rev:2});await first;clearTimeout(a.saveTimer);assert.equal(a.dirty,true);
 const second=a.doSave();await tick();assert.equal(requests[1].body.rev,2);assert.equal(requests[1].body.tracks[0].name,'second');
 requests[1].d.resolve({rev:3});await second;assert.equal(a.rev,3);assert.equal(a.dirty,false);
}
// Disjoint remote changes retain a newly created local track. Conflicts pause saving.
{
 const {a,initialize,drafts}=harness();initialize();a.tracks.push({id:'local',clips:[]});a.dirty=true;
 const remote=copy(a._saves.A.base);remote.rev=2;remote.tracks[0].name='AI';a.applyRemoteArrangement(remote,['tracks']);
 assert.equal(a.tracks[0].name,'AI');assert.equal(a.tracks[1].id,'local');clearTimeout(a.saveTimer);
 a.tracks[0].name='local edit';remote.rev=3;remote.tracks[0].name='remote edit';a.applyRemoteArrangement(remote,['tracks']);await tick();
 assert.ok(a.saveConflict);assert.equal(drafts.get('A').local.tracks[0].name,'local edit');assert.equal(drafts.get('A').remote.tracks[0].name,'remote edit');
 await a.doSave();assert.ok(a.saveConflict);assert.equal(a.dirty,true);
}
// UI ops wait for the save that makes a local track visible to the server.
{
 const save=deferred(),calls=[];const {a,window,initialize}=harness(undefined,()=>{calls.push('save');return save.promise});initialize();a.dirty=true;
 window.UI.postJSON=async()=>{calls.push('ops');return {}};const op=a.arrangeOps([]);await tick();assert.deepEqual(calls,['save']);
 save.resolve({rev:2});await op;assert.deepEqual(calls,['save','ops']);
}
// Resolve numeric UI positions before waiting: reordering cannot retarget an operation.
{
 const save=deferred(),sent=[];const {a,window,initialize}=harness(undefined,()=>save.promise);initialize();a.tracks.push({id:'two',clips:[]});a.dirty=true;
 window.UI.postJSON=async(url,body)=>{sent.push(body.ops[0]);return {}};
 const operation=a.arrangeOps([{op:'setTrackMix',track:0,gain:.2}]);await tick();a.tracks.reverse();save.resolve({rev:2});await operation;assert.equal(sent[0].track,'one');clearTimeout(a.saveTimer);
}
// A delayed preview cannot sound after a project switch.
{
 const {a,window,initialize}=harness();initialize();const messages=[];
 window.UI.toast=message=>messages.push(message);window.AudioBackend={isEngine:()=>true,isEngineReady:()=>true,isNativePreferred:()=>true};
 a.audioSync='ready';a.engine.ensureTrack=()=>({sfLoading:true,_useNative:false});a.startPlayback();
 assert.equal(a.isPlaying,false);assert.ok(messages.some(m=>m.includes('音源正在同步')));
}
// A delayed preview cannot sound after a project switch.
{
 const {a,window,initialize}=harness();initialize();const loading=deferred(),notes=[];
 window.AudioBackend={isEngine:()=>true,isEngineReady:()=>true};window.EngineBridge={setTrackVoice:()=>loading.promise,noteOnTrack:()=>notes.push('on'),noteOffTrack:noop};
 a.previewRackSource({ok:true,voice:{type:'synth',wave:'sine'}});a.projectId='B';a.projectEpoch++;
 loading.resolve();await tick();assert.deepEqual(notes,[]);
}
// A mixer window stays visible when the arrangement window is closed.
{
 const {a,window,initialize}=harness();initialize();const updates=[];
 window.ArrMixer={syncTracks:()=>updates.push('controls')};window.PluginPanel={restoreAll:()=>updates.push('audio')};
 const remote=copy(a._saves.A.base);remote.rev=2;remote.mixer={channels:{},master:{gain:.2,fx:[]}};
 a.applyRemoteArrangement(remote,['mixer']);assert.equal(a.mixer.master.gain,.2);assert.ok(updates.includes('controls'));assert.ok(updates.includes('audio'));
}
// A piano-roll operation callback from an old project cannot start a current save or show an error.
for (const success of [true, false]) {
 const {a,window,initialize}=harness();initialize();const response=deferred(),effects=[];
 a.arrangeOps=()=>response.promise;a.scheduleSave=()=>effects.push('save');window.UI.toast=()=>effects.push('toast');
 a.setPianoRollSource('instrument','0');a.projectId='B';a.projectEpoch++;
 if(success)response.resolve({});else response.reject(new Error('old operation'));await tick();assert.deepEqual(effects,[]);
}
console.log('arrangement save: generations, serial saves, pending edits, conflicts, ops ordering and remote mixer controls passed');
