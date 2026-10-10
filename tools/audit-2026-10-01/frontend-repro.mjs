// Positive regressions for the original 12 frontend observations. Eight controller/plugin
// observations are exercised by the imported behavior suites, followed by four focused probes.
import fs from 'node:fs';import vm from 'node:vm';import assert from 'node:assert/strict';
import '../dev/arrangement_save_test.mjs';import '../dev/plugin_lifecycle_test.mjs';
const read=p=>fs.readFileSync(new URL('../../'+p,import.meta.url),'utf8');
const tick=()=>new Promise(r=>setTimeout(r,20));const noop=()=>{};
function passed(name,detail){console.log(`PASS ${name}: ${detail}`);}
function panel(installed = [], vst = []) {
  const calls = [], warnings = [];
  const win = {
    setTimeout, clearTimeout, setInterval, clearInterval,
    UI: {
      getJSON: async url => ({plugins: url.endsWith('/installed') ? installed : vst}),
      postJSON: async (url,body) => { calls.push({url,body}); return {}; },
      toast: (message,kind) => warnings.push({message,kind}),
    },
  };
  // Expose private resetAll/state in memory only; do not rewrite the emitted file.
  const source = read('frontend/js/arrangement/plugin_panel.js').replace(
    'window.PluginPanel = {', 'window.__auditPanel = { state: state, resetAll: resetAll }; window.PluginPanel = {');
  vm.runInNewContext(source, {window:win, document:{}, console});
  return {win,calls,warnings};
}

{
  const {win,calls} = panel();
  const state=win.__auditPanel.state;
  state.target={track:0,slot:0,master:false};
  state.rows=[{index:0,default:0.5,min:0,max:2,sliderEl:{setAttribute:noop},valueEl:{}}];
  win.__auditPanel.resetAll();
  await tick();
  const param=calls.find(c=>c.url.endsWith('/param'));
  assert.equal(param.body.value,0.5); // physical value becomes 2, should be normalized 0.5
  passed('MM-RESET-NORMALIZATION', 'default resets to normalized 0.5');
}

{
  let reads=0,parses=0;
  const win={AudioBackend:{isNativePreferred:()=>false,isEngine:()=>false},
    SoundRegistry:{get:()=>({id:'sf2_registry_id'}),diskPathOf:()=> 'C:/audit/font.sf2',loadSf2:async()=>{reads++;return {data:new ArrayBuffer(0)};}},
    SoundLibrary:{getSoundFont:async()=>{reads++;return {data:new ArrayBuffer(0)};}},
  };
  vm.runInNewContext(read('frontend/js/arrangement/audio_engine.js'),{window:win,console,setTimeout,clearTimeout,setInterval,clearInterval});
  const engine=new win.ArrangeEngine();
  const track={id:'T',source:{sourceId:'sf2_registry_id',presetId:'0:0'}};
  const nodes={soundfont:{parseSF2:()=>{parses++;return true;},setPreset:noop}};
  engine.trackNodes.T=nodes;engine.getTracks=()=>[track];
  engine.loadTrackSoundFont(track,nodes,track.source);
  await tick(); assert.equal(reads,1); assert.equal(parses,1);
  passed('BROWSER-SF2-SILENT', 'v2 SF2 retrieves and decodes by sourceId');
}

{
  const html=read('frontend/mm-ui.html');
  const start=html.indexOf('function onPluginMessage(ev) {');
  const end=html.indexOf('window.addEventListener("message", onPluginMessage);',start);
  assert.ok(start>=0 && end>start);
  const writes=[];
  const sandbox={paramsById:{gain:{index:0}},setParam:(index,value)=>writes.push({index,value}),
    dropped:0,elStatus:{},elSkeleton:{},frame:{contentWindow:{},style:{}},loadParams:async()=>{},Math};
  vm.runInNewContext(html.slice(start,end)+'\nthis.handler=onPluginMessage;',sandbox);
  sandbox.handler({source:{},origin:'https://untrusted.invalid',data:{type:'mm.setParam',id:'gain',value:0}});
  assert.deepEqual(writes,[]);
  sandbox.handler({source:sandbox.frame.contentWindow,data:{type:"mm.setParam",id:"gain",value:0}});assert.deepEqual(writes,[{index:0,value:0}]);
  passed('MM-POSTMESSAGE-SOURCE', 'only the target iframe can write parameters');
}

{
  const js=read('frontend/js/chat.js');
  const start=js.indexOf('function applyArrangementEvent(ev');
  const end=js.indexOf('function pullArrangementRemote(',start);
  assert.ok(start>=0 && end>start);
  const timers=new Map(), pulls=[], revisions=[];
  let sequence=0;
  const sandbox={currentProjectId:'P',window:{Arrange:{projectId:'P',rev:0}},UI:{toast:noop},
    appendSysLine:noop,arrangementPullTimer:null,arrangementPullProject:"",arrangementChanged:{},arrangementPullRev:0,pullArrangementRemote:(x,rev)=>{pulls.push(x);revisions.push(rev);},
    setTimeout:fn=>{const id=++sequence;timers.set(id,fn);return id;},clearTimeout:id=>timers.delete(id)};
  vm.runInNewContext(js.slice(start,end)+'\nthis.handler=applyArrangementEvent;',sandbox);
  sandbox.handler({project:'P',rev:1,changed:['rack']});
  sandbox.handler({project:'P',rev:2,changed:['mixer']});
  sandbox.handler({project:'P',rev:1,changed:['tracks']});
  for (const fn of timers.values()) fn();
  assert.equal(JSON.stringify(pulls.map(x=>x.sort())), '[["mixer","rack","tracks"]]');
  assert.deepEqual(revisions,[2]);
  passed('SSE-DEBOUNCE-LOST-CHANGE', 'all changed categories and highest revision are retained');
}

console.log('frontend audit regressions passed');
