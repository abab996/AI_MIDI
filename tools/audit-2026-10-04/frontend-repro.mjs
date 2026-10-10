import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import assert from 'node:assert/strict';
import ts from 'typescript';
import blank from 'ts-blank-space';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const read = p => fs.readFileSync(path.join(root, p), 'utf8');
const noop = () => {};
const tick = () => new Promise(r => setImmediate(r));
function confirmed(name, detail) { console.log(`CONFIRMED ${name}: ${detail}`); }
function nodeText(file, predicate) {
  const tree = ts.createSourceFile(file, read(file), ts.ScriptTarget.Latest, true, ts.ScriptKind.JS);
  let found;
  function visit(node) { if (!found && predicate(node, tree)) found = node.getText(tree); if (!found) ts.forEachChild(node, visit); }
  visit(tree); assert.ok(found, `Missing source node: ${file}`); return found;
}
const privateFunction = (file, name) => nodeText(file, n => ts.isFunctionDeclaration(n) && n.name?.text === name);
const piano = 'frontend/js/pianoroll/pianoroll.js';
const method = name => nodeText(piano, (n, tree) => ts.isExpressionStatement(n) && ts.isBinaryExpression(n.expression) && n.expression.left.getText(tree) === `PianoRoll.prototype.${name}`);
function pianoContext(names, extra = {}) {
  const context = { PianoRoll: function () {}, UI: {}, window: { UI: { toast: noop } }, document: {}, setTimeout, clearTimeout, console, ...extra };
  vm.createContext(context); vm.runInContext(names.map(method).join('\n'), context); return context;
}

// Compare emitted assets without modifying any output file.
{
  const files = [];
  function walk(dir) { for (const ent of fs.readdirSync(dir, { withFileTypes: true })) { const p=path.join(dir, ent.name); if(ent.isDirectory()) { if(ent.name !== 'types') walk(p); } else if(p.endsWith('.ts') && !p.endsWith('.d.ts')) files.push(p); } }
  const sourceRoot=path.join(root,'frontend/src'); walk(sourceRoot);
  const mismatches=files.filter(p => { const out=path.join(root,'frontend/js',path.relative(sourceRoot,p).replace(/\.ts$/,'.js')); return !fs.existsSync(out) || blank(fs.readFileSync(p,'utf8')) !== fs.readFileSync(out,'utf8'); });
  assert.deepEqual(mismatches, []);
  console.log(`PASS TS-JS: ${files.length} emitted assets match current sources`);
}

{
  const timers = new Map(); let timerId=0, saved=[];
  const context=pianoContext(['scheduleAutoSave','saveCurrentTab'], { setTimeout: fn=>{timers.set(++timerId,fn);return timerId;}, clearTimeout:id=>timers.delete(id) });
  const a={id:'A',dirty:true}, b={id:'B',dirty:false};
  const controller=Object.create(context.PianoRoll.prototype);
  Object.assign(controller,{active:a,setSaveState:noop,getActiveTab(){return this.active;},saveTab:async tab=>{saved.push(tab.id);if(tab.dirty)tab.dirty=false;}});
  controller.scheduleAutoSave(); controller.active=b;
  for(const fn of timers.values())fn(); await tick();
  assert.deepEqual(saved,['B']); assert.equal(a.dirty,true);
  confirmed('PIANO-DEBOUNCE-TAB','A edited, switched to B before 800ms: only B is saved and A remains dirty');
}

{
  let reject; const request=new Promise((_,r)=>{reject=r;});
  const context=pianoContext(['saveTab','closeTab'],{UI:{postJSON:()=>request}});
  const tab={id:'A',dirty:true,projectId:'P',fullName:'song.mid',bpm:150,notes:[{note:'C4',velocity:90,start:0,end:1}]};
  const controller=Object.create(context.PianoRoll.prototype);
  Object.assign(controller,{tabs:[tab],activeTabId:'A',setSaveState:noop,renderTabs:noop,close:noop});
  const pending=controller.saveTab(tab).catch(()=>{});
  assert.equal(tab.dirty,false); controller.closeTab('A',null); assert.equal(controller.tabs.length,0);
  reject(new Error('simulated write failure')); await pending;
  assert.equal(tab.dirty,true); assert.equal(controller.tabs.length,0);
  confirmed('PIANO-CLOSE-INFLIGHT','pending save clears dirty; close removes tab, later failure has no recoverable UI tab');
}

{
  const requests=[]; let disk;
  const context=pianoContext(['saveTab'],{UI:{postJSON:(_url,body)=>new Promise(resolve=>requests.push({body,finish(){disk=body.note_table;resolve({});}}))}});
  const tab={id:'A',dirty:true,projectId:'P',fullName:'song.mid',bpm:150,notes:[{note:'C4',velocity:90,start:0,end:1}]};
  const controller=Object.create(context.PianoRoll.prototype);Object.assign(controller,{setSaveState:noop,renderTabs:noop});
  const older=controller.saveTab(tab); tab.notes[0].note='D4';tab.dirty=true;const newer=controller.saveTab(tab);
  assert.equal(requests.length,2);requests[1].finish();await newer;requests[0].finish();await older;
  assert.match(disk,/C4/);assert.equal(tab.dirty,false);assert.equal(tab.notes[0].note,'D4');
  confirmed('PIANO-SAVE-REORDER','concurrent saves can leave old C4 on disk while D4 tab reports clean');
}

{
  const bytes=new Uint8Array([77,84,104,100,0,0,0,6,0,0,0,1,1,224,77,84,114,107,0,0,0,20,0,255,81,3,6,26,128,0,144,60,90,131,96,128,60,0,0,255,47,0]);
  const context=pianoContext(['openFile','parseMidiBytes'],{fetch:async()=>({ok:true,arrayBuffer:async()=>bytes.buffer})});
  vm.runInContext(read('frontend/js/arrangement/midi_parse.js'),context);
  const controller=Object.create(context.PianoRoll.prototype);
  Object.assign(controller,{tabs:[],switchTab:noop,open:noop});
  controller.openFile('P','tempo-150.mid','',noop);await tick();await tick();
  assert.equal(controller.tabs[0].notes.length,1);
  assert.equal(controller.tabs[0].bpm,120);
  confirmed('PIANO-TEMPO-RESET','openFile always initializes bpm=120; original MIDI tempo is not returned by the note-only parser');
}

// The desktop bridge does not subscribe to the caller's abort signal.
{
  const handlers=new Map();let ends=0;const controller=new AbortController();
  const win={UI:{},go:{app:{App:{ChatStreamStart:async()=>{},AnswerStreamStart:async()=>{}}}},runtime:{EventsOn:(n,fn)=>handlers.set(n,fn),EventsOff:n=>handlers.delete(n)}};
  const context={window:win,UI:win.UI,console,SSE_TIMEOUT_MS:300000,Date,Math,Promise,setTimeout:()=>1,clearTimeout:noop,warnedBridgeFallback:false};
  vm.createContext(context);vm.runInContext(privateFunction('frontend/js/app.js','streamStart')+'\nthis.start=streamStart;',context);
  const pending=context.start('chat',{project_id:'P',message:'test'},noop,()=>{ends++;},{signal:controller.signal});
  await tick();controller.abort();await tick();
  assert.equal(handlers.size,2);assert.equal(ends,0);
  confirmed('DESKTOP-BRIDGE-ABORT','AbortSignal abort leaves both old IPC listeners and onEnd subscription active');
  handlers.get([...handlers.keys()].find(n=>n.startsWith('chat:end:')))(); await pending;
}

// Old onEnd closures call a function with shared mutable epoch fields.
{
  const el={classList:{remove:noop},focus:noop};
  const context={streamEnded:false,chatEpoch:2,streamEpoch:2,chatBusy:true,liveStreaming:true,abortController:{newStream:true},resetStreamDelta:noop,
    userToggledStream:true,streamTrack:{el:null},lastMessages:[],streamSysLines:[],autoGrowInput:noop,updateScrollBtn:noop,$:()=>el,console};
  vm.createContext(context);vm.runInContext(privateFunction('frontend/js/chat.js','chatDone')+'\nthis.done=chatDone;',context);
  const oldOnEnd=()=>context.done();oldOnEnd();
  assert.equal(context.chatBusy,false);assert.equal(context.liveStreaming,false);assert.equal(context.abortController,null);assert.equal(context.streamEnded,true);
  confirmed('CHAT-OLD-END-NEW-STREAM','old onEnd sees the new shared streamEpoch and ends the new stream');
}

// Request headers supplied by a Request object are discarded by the fetch wrapper.
{
  let forwarded;
  const win={location:{href:'http://127.0.0.1/chat.html',origin:'http://127.0.0.1',hash:'',pathname:'/chat.html',search:''},fetch:async(input,init)=>{forwarded={input,init};return new Response('{}');}};
  class XHR { open(){} setRequestHeader(){} send(){this.status=200;this.responseText='{"token":"audit"}';} }
  const context={window:win,sessionStorage:{getItem:()=>'',setItem:noop},XMLHttpRequest:XHR,Headers,URL,URLSearchParams,history:{replaceState:noop}};
  vm.runInNewContext(read('frontend/js/authfetch.js'),context);
  await win.fetch(new Request('http://127.0.0.1/api/test',{headers:{'Content-Type':'application/json','X-Custom':'kept'}}));
  assert.equal(forwarded.init.headers.get('Content-Type'),null);assert.equal(forwarded.init.headers.get('X-Custom'),null);
  confirmed('AUTHFETCH-REQUEST-HEADERS','wrapping a Request installs a new headers collection and loses the Request headers');
}

{
  const handlers=new Map(),events=[];
  const file='frontend/js/pianoroll/midi_input.js';
  const source=nodeText(file,(n,tree)=>ts.isExpressionStatement(n) && ts.isBinaryExpression(n.expression) && n.expression.left.getText(tree)==='MidiInputRouter.prototype.bindTypingKeyboard');
  const context={MidiInputRouter:function(){},window:{PianoRoll:{isOpen:true},addEventListener:(name,fn)=>handlers.set(name,fn)}};
  vm.createContext(context);vm.runInContext(source,context);
  const router=Object.create(context.MidiInputRouter.prototype);
  Object.assign(router,{settings:{typingKeyboard:true},baseOctave:4,pressedKeys:{},heldNotes:{},emit:(...args)=>events.push(args)});
  router.bindTypingKeyboard();handlers.get('keydown')({code:'KeyZ',target:{tagName:'BODY'}});
  handlers.get('blur')?.(); // Window loses focus; browser keyup is delivered elsewhere.
  handlers.get('keydown')({code:'KeyZ',target:{tagName:'BODY'}});
  assert.equal(events.length,1);assert.equal(events[0][0],'noteon');assert.equal(router.pressedKeys.KeyZ,60);
  confirmed('MIDI-BLUR-STUCK','window blur emits no noteoff; KeyZ remains held and the next press is suppressed');
}

console.log('Frontend observations completed; CONFIRMED denotes an existing bug, not a fix.');
