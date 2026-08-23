/* SoundFont 2 (.sf2) 二进制解析与 Web Audio 采样播放器 */
(function (window) {
  "use strict";

  var AudioContext = window.AudioContext || window.webkitAudioContext;

  function SoundFontPlayer(sharedCtx, outNode) {
    this.ctx = null;
    this._sharedCtx = sharedCtx || null; // 多引擎共享同一 AudioContext（编排窗口多轨场景）
    this._outNode = outNode || null;     // 输出目标节点，缺省接 ctx.destination
    this.masterGain = null;
    this.activeVoices = {}; // midiNote -> [{ source, gain, stopTime }]
    this.loadedPresets = []; // [{ id, name, bank, program, zones }]
    this.currentPreset = null;
    this.volume = 0.75;
    this.isMuted = false;
    this.sampleBuffers = {}; // sampleId -> { buffer, info }（首次触发时惰性转换）
    this._sampleData = null; // smpl 块的 Int16Array 视图
    this._rawSamples = [];   // shdr 采样头列表
    this.builtinInstrument = "piano"; // "piano", "strings", "epiano", "bass"
  }

  SoundFontPlayer.prototype.init = function () {
    if (this.ctx) return;
    try {
      this.ctx = this._sharedCtx || new AudioContext();
      this.masterGain = this.ctx.createGain();
      this.masterGain.gain.setValueAtTime(this.volume, this.ctx.currentTime);
      this.masterGain.connect(this._outNode || this.ctx.destination);
      this.builtinBuffers = null;   // 惰性生成：见 ensureBuiltinPresets
    } catch (e) {
      console.warn("SoundFontPlayer AudioContext error:", e);
    }
  };

  SoundFontPlayer.prototype.resume = function () {
    if (!this.ctx) this.init();
    if (this.ctx && this.ctx.state === "suspended") {
      return this.ctx.resume();
    }
    return Promise.resolve();
  };

  SoundFontPlayer.prototype.setVolume = function (vol) {
    this.volume = Math.max(0, Math.min(1, vol));
    if (this.masterGain && this.ctx) {
      this.masterGain.gain.setTargetAtTime(this.isMuted ? 0 : this.volume, this.ctx.currentTime, 0.01);
    }
  };

  /* ═══════════ SF2 二进制解析器 ═══════════ */

  function BinaryReader(arrayBuffer) {
    this.view = new DataView(arrayBuffer);
    this.pos = 0;
    this.length = arrayBuffer.byteLength;
  }

  BinaryReader.prototype.readFourCC = function () {
    if (this.pos + 4 > this.length) return "";
    var s = "";
    for (var i = 0; i < 4; i++) {
      s += String.fromCharCode(this.view.getUint8(this.pos++));
    }
    return s;
  };

  BinaryReader.prototype.readUint32 = function () {
    var v = this.view.getUint32(this.pos, true);
    this.pos += 4;
    return v;
  };

  BinaryReader.prototype.readUint16 = function () {
    var v = this.view.getUint16(this.pos, true);
    this.pos += 2;
    return v;
  };

  BinaryReader.prototype.readInt16 = function () {
    var v = this.view.getInt16(this.pos, true);
    this.pos += 2;
    return v;
  };

  BinaryReader.prototype.readUint8 = function () {
    return this.view.getUint8(this.pos++);
  };

  BinaryReader.prototype.readFixedString = function (len) {
    var s = "";
    var end = Math.min(this.pos + len, this.length);
    for (var i = this.pos; i < end; i++) {
      var c = this.view.getUint8(i);
      if (c === 0) break;
      s += String.fromCharCode(c);
    }
    this.pos += len;
    return s.trim();
  };

  /* 最小 zone 链解析：pbag/pgen（preset→instrument）+ inst/ibag/igen
     （instrument→采样与键区间）。任何一块缺失返回 null——调用方回退到
     旧的"全库最近根音"匹配。键区间匹配修复：旋律预设误选鼓组采样。 */
  function parseSF2Zones(chunks, reader, rawPresets) {
    if (!chunks["pbag"] || !chunks["pgen"] || !chunks["inst"] ||
        !chunks["ibag"] || !chunks["igen"] || !rawPresets.length) {
      return null;
    }

    function readNdx(chunkId, recordSize, ndxOffset) {
      var arr = [];
      var end = chunks[chunkId].pos + chunks[chunkId].size;
      reader.pos = chunks[chunkId].pos;
      while (reader.pos + recordSize <= end) {
        reader.pos += ndxOffset;
        arr.push(reader.readUint16());
        reader.pos += recordSize - ndxOffset - 2;
      }
      return arr;
    }

    var pbagGenNdx = readNdx("pbag", 4, 0);
    var ibagGenNdx = readNdx("ibag", 4, 0);
    var instBagNdx = readNdx("inst", 38, 20);

    function readGens(chunkId, lo, hi) {
      var out = [];
      var base = chunks[chunkId].pos;
      var end = base + chunks[chunkId].size;
      var count = hi - lo;
      if (count <= 0) return out;
      reader.pos = base + lo * 4;
      if (reader.pos >= end) return out;
      count = Math.min(count, Math.floor((end - reader.pos) / 4));
      for (var g = 0; g < count; g++) {
        var oper = reader.readUint16();
        var amount = reader.view.getInt16(reader.pos, true);
        reader.pos += 2;
        out.push({ oper: oper, amount: amount });
      }
      return out;
    }

    // inst → zone 列表（sampleID=53 / keyRange=43 / overridingRootKey=58）
    var instZones = [];
    for (var ii = 0; ii < instBagNdx.length; ii++) {
      var zones = [];
      var bagLo = instBagNdx[ii];
      var bagHi = instBagNdx[ii + 1] !== undefined ? instBagNdx[ii + 1] : bagLo + 1;
      for (var b = bagLo; b < bagHi && b < ibagGenNdx.length; b++) {
        var gLo = ibagGenNdx[b];
        var gHi = ibagGenNdx[b + 1] !== undefined ? ibagGenNdx[b + 1] : gLo;
        var gens = readGens("igen", gLo, gHi);
        var zone = { sampleId: -1, keyLo: 0, keyHi: 127, root: -1 };
        for (var gi = 0; gi < gens.length; gi++) {
          var gen = gens[gi];
          if (gen.oper === 53) zone.sampleId = gen.amount;
          else if (gen.oper === 43) {
            zone.keyLo = gen.amount & 0xFF;
            zone.keyHi = (gen.amount >> 8) & 0xFF;
          } else if (gen.oper === 58) zone.root = gen.amount;
        }
        if (zone.sampleId >= 0) zones.push(zone);
      }
      instZones.push(zones);
    }

    // preset → 乐器（instrument 操作符 41），聚合各乐器的 zone
    var presetZones = [];
    for (var pi = 0; pi < rawPresets.length; pi++) {
      var pLo = rawPresets[pi].bagIndex;
      var pHi = rawPresets[pi + 1] !== undefined ? rawPresets[pi + 1].bagIndex : pLo + 1;
      var zs = [];
      for (var pb = pLo; pb < pHi && pb < pbagGenNdx.length; pb++) {
        var pgLo = pbagGenNdx[pb];
        var pgHi = pbagGenNdx[pb + 1] !== undefined ? pbagGenNdx[pb + 1] : pgLo;
        var pgens = readGens("pgen", pgLo, pgHi);
        for (var pj = 0; pj < pgens.length; pj++) {
          if (pgens[pj].oper === 41) {
            var iIdx = pgens[pj].amount;
            if (iIdx >= 0 && iIdx < instZones.length) {
              zs = zs.concat(instZones[iIdx]);
            }
          }
        }
      }
      presetZones.push(zs);
    }
    return presetZones;
  }

  SoundFontPlayer.prototype.parseSF2 = function (arrayBuffer) {
    var reader = new BinaryReader(arrayBuffer);
    if (reader.readFourCC() !== "RIFF") throw new Error("无效的 RIFF 文件头");
    reader.readUint32(); // riff size
    if (reader.readFourCC() !== "sfbk") throw new Error("不是有效的 SoundFont 2 (sfbk) 格式");

    var sampleData = null;
    var rawSamples = [];
    var rawInsts = [];
    var rawPresets = [];
    var fontName = "Custom SoundFont";
    var pdtaChunksRef = null;   // zone 解析需要（提升出 pdta 分支作用域）

    while (reader.pos + 8 <= reader.length) {
      var chunkId = reader.readFourCC();
      var chunkSize = reader.readUint32();
      var nextPos = reader.pos + chunkSize;

      if (chunkId === "LIST") {
        var listType = reader.readFourCC();
        var listEnd = nextPos;

        if (listType === "INFO") {
          while (reader.pos + 8 <= listEnd) {
            var subId = reader.readFourCC();
            var subSize = reader.readUint32();
            var subEnd = reader.pos + subSize;
            if (subId === "INAM") {
              fontName = reader.readFixedString(subSize) || fontName;
            }
            reader.pos = subEnd;
          }
        } else if (listType === "sdta") {
          while (reader.pos + 8 <= listEnd) {
            var sId = reader.readFourCC();
            var sSize = reader.readUint32();
            if (sId === "smpl") {
              sampleData = new Int16Array(arrayBuffer, reader.pos, sSize / 2);
            }
            reader.pos += sSize;
          }
        } else if (listType === "pdta") {
          var pdtaChunks = {};
          while (reader.pos + 8 <= listEnd) {
            var pId = reader.readFourCC();
            var pSize = reader.readUint32();
            pdtaChunks[pId] = { pos: reader.pos, size: pSize };
            reader.pos += pSize;
          }
          pdtaChunksRef = pdtaChunks;

          // 解析 sample headers (shdr)
          if (pdtaChunks["shdr"] && sampleData) {
            var shdrPos = pdtaChunks["shdr"].pos;
            var shdrEnd = shdrPos + pdtaChunks["shdr"].size;
            reader.pos = shdrPos;
            while (reader.pos + 46 <= shdrEnd) {
              var sName = reader.readFixedString(20);
              var start = reader.readUint32();
              var end = reader.readUint32();
              var startLoop = reader.readUint32();
              var endLoop = reader.readUint32();
              var sampleRate = reader.readUint32();
              var origPitch = reader.readUint8();
              var pitchAdj = reader.view.getInt8(reader.pos++);
              reader.pos += 4; // link & sampleType

              if (end > start && sampleRate > 0) {
                rawSamples.push({
                  name: sName,
                  start: start,
                  end: end,
                  startLoop: startLoop,
                  endLoop: endLoop,
                  sampleRate: sampleRate,
                  originalPitch: origPitch || 60,
                  pitchCorrection: pitchAdj
                });
              }
            }
          }

          // 解析 presets (phdr)
          if (pdtaChunks["phdr"]) {
            var phdrPos = pdtaChunks["phdr"].pos;
            var phdrEnd = phdrPos + pdtaChunks["phdr"].size;
            reader.pos = phdrPos;
            while (reader.pos + 38 <= phdrEnd) {
              var pName = reader.readFixedString(20);
              var pNum = reader.readUint16();
              var pBank = reader.readUint16();
              var pBagIdx = reader.readUint16();
              reader.pos += 12; // library, genre, morph
              if (pName && pName !== "EOP") {
                rawPresets.push({
                  name: pName,
                  program: pNum,
                  bank: pBank,
                  bagIndex: pBagIdx
                });
              }
            }
          }
        }
      }
      reader.pos = nextPos;
    }

    // 惰性转换：仅保留原始采样数据引用，首次触发某采样时才转 AudioBuffer（大体积 SF2 不再卡 UI）
    this.sampleBuffers = {};
    this._sampleData = sampleData || null;
    this._rawSamples = rawSamples;

    // zone 链解析失败（文件缺块）时保持 null → noteOn 回退全库匹配
    var zoneList = null;
    try {
      zoneList = parseSF2Zones(pdtaChunksRef, reader, rawPresets);
    } catch (e) {
      zoneList = null;
    }

    var resultPresets = rawPresets.map(function (p, idx) {
      return {
        id: "sf2_" + p.bank + "_" + p.program + "_" + idx,
        name: p.name || ("Preset " + p.program),
        bank: p.bank,
        program: p.program,
        fontName: fontName,
        zones: zoneList && zoneList[idx] && zoneList[idx].length ? zoneList[idx] : null,
        sampleIndices: rawSamples.map(function (_, i) { return i; })
      };
    });

    if (!resultPresets.length && rawSamples.length > 0) {
      resultPresets.push({
        id: "sf2_default",
        name: fontName || "SoundFont Instrument",
        bank: 0,
        program: 0,
        fontName: fontName,
        sampleIndices: rawSamples.map(function (_, i) { return i; })
      });
    }

    this.loadedPresets = resultPresets;
    if (resultPresets.length) {
      this.currentPreset = resultPresets[0];
    }
    return { name: fontName, presets: resultPresets };
  };

  /* 取指定索引的采样 AudioBuffer，首次访问时从 Int16 原始数据转换并缓存 */
  SoundFontPlayer.prototype.getSampleBuffer = function (idx) {
    var cached = this.sampleBuffers[idx];
    if (cached) return cached;
    var sm = this._rawSamples ? this._rawSamples[idx] : null;
    if (!sm || !this.ctx || !this._sampleData) return null;
    var len = sm.end - sm.start;
    if (len <= 0) return null;
    try {
      var audioBuf = this.ctx.createBuffer(1, len, sm.sampleRate);
      var channelData = audioBuf.getChannelData(0);
      var srcStart = sm.start;
      for (var i = 0; i < len; i++) {
        channelData[i] = this._sampleData[srcStart + i] / 32768.0;
      }
      var entry = { buffer: audioBuf, info: sm };
      this.sampleBuffers[idx] = entry;
      return entry;
    } catch (err) {
      console.warn("Buffer creation failed for sample", sm.name, err);
      return null;
    }
  };

  /* ═══════════ 高品质内置合成预设 (无需外部文件即可发声) ═══════════ */

  /* 内置采样按需合成（约 120 万次 sin 调用）：此前在 init（页面加载）
     同步执行，阻塞整个聊天页首帧；改为首次选用/触发内置音色时才生成 */
  SoundFontPlayer.prototype.ensureBuiltinPresets = function () {
    if (this.builtinBuffers) return;
    this.generateBuiltinPresets();
  };

  SoundFontPlayer.prototype.generateBuiltinPresets = function () {
    if (!this.ctx) return;
    this.builtinBuffers = {};

    var sampleRates = this.ctx.sampleRate;
    // 生成精美三角钢琴采样 (基于泛音物理模型采样)
    var pianoLen = sampleRates * 2.5;
    var pBuf = this.ctx.createBuffer(1, pianoLen, sampleRates);
    var pData = pBuf.getChannelData(0);
    var f0 = 261.63; // C4
    for (var i = 0; i < pianoLen; i++) {
      var t = i / sampleRates;
      var env = Math.exp(-t * 3.2);
      // 多次谐波叠加
      var val = Math.sin(2 * Math.PI * f0 * t) * 0.6 +
                Math.sin(2 * Math.PI * f0 * 2 * t) * 0.25 * Math.exp(-t * 4.5) +
                Math.sin(2 * Math.PI * f0 * 3 * t) * 0.15 * Math.exp(-t * 6.0) +
                Math.sin(2 * Math.PI * f0 * 4 * t) * 0.08 * Math.exp(-t * 7.5);
      pData[i] = val * env;
    }
    this.builtinBuffers["piano"] = { buffer: pBuf, basePitch: 60 };

    // 生成温暖弦乐采样 (Strings)
    var strLen = sampleRates * 3.0;
    var sBuf = this.ctx.createBuffer(1, strLen, sampleRates);
    var sData = sBuf.getChannelData(0);
    for (var j = 0; j < strLen; j++) {
      var st = j / sampleRates;
      var senv = Math.min(st / 0.4, 1.0) * (j > strLen - sampleRates * 0.6 ? (strLen - j) / (sampleRates * 0.6) : 1.0);
      var sval = 0;
      for (var h = 1; h <= 8; h++) {
        var detune = 1 + (h % 2 === 0 ? 0.003 : -0.003);
        sval += (1 / h) * Math.sin(2 * Math.PI * f0 * h * detune * st);
      }
      sData[j] = sval * 0.22 * senv;
    }
    this.builtinBuffers["strings"] = { buffer: sBuf, basePitch: 60 };
  };

  SoundFontPlayer.prototype.setPreset = function (presetOrBuiltinId) {
    if (presetOrBuiltinId === "piano" || presetOrBuiltinId === "strings") {
      this.ensureBuiltinPresets();
    }
    if (this.builtinBuffers && this.builtinBuffers[presetOrBuiltinId]) {
      this.builtinInstrument = presetOrBuiltinId;
      this.currentPreset = null;
      return;
    }
    var found = this.loadedPresets.find(function (p) { return p.id === presetOrBuiltinId; });
    if (found) {
      this.currentPreset = found;
      this.builtinInstrument = null;
    }
  };

  SoundFontPlayer.prototype.noteOn = function (midiNote, velocity, when) {
    this.resume();
    if (!this.ctx || this.isMuted) return;

    var vel = (velocity !== undefined ? velocity : 100) / 127;
    vel = Math.max(0.01, Math.min(1, vel));
    var startTime = when !== undefined ? when : this.ctx.currentTime;

    var targetBuffer = null;
    var basePitch = 60;
    var sampleInfo = null;

    if (this.currentPreset && this._rawSamples && this._rawSamples.length) {
      var bestIdx = -1;
      var zones = this.currentPreset.zones && this.currentPreset.zones.length
        ? this.currentPreset.zones : null;
      if (zones) {
        // 键区间匹配：优先取音符落入区间的采样（此前全库按最近根音挑，
        // 会把鼓组等其它乐器的采样串进旋律音色）
        var bestScore = Infinity;
        for (var z = 0; z < zones.length; z++) {
          var zo = zones[z];
          if (zo.sampleId >= this._rawSamples.length) continue;
          if (midiNote < zo.keyLo || midiNote > zo.keyHi) continue;
          var root = zo.root >= 0 ? zo.root : this._rawSamples[zo.sampleId].originalPitch;
          var dz = Math.abs(midiNote - root);
          if (dz < bestScore) { bestScore = dz; bestIdx = zo.sampleId; }
        }
        if (bestIdx < 0) bestIdx = zones[0].sampleId;   // 区间外：用首个 zone 兜底
      } else {
        // 回退：文件缺 zone 块时全库最近根音
        var bestDiff = 999;
        for (var k = 0; k < this._rawSamples.length; k++) {
          var diff = Math.abs(midiNote - this._rawSamples[k].originalPitch);
          if (diff < bestDiff) {
            bestDiff = diff;
            bestIdx = k;
          }
        }
      }
      if (bestIdx >= 0 && bestIdx < this._rawSamples.length) {
        // 惰性转换：仅触发到的采样才建 Buffer
        var selected = this.getSampleBuffer(bestIdx);
        if (selected) {
          targetBuffer = selected.buffer;
          basePitch = selected.info.originalPitch;
          sampleInfo = selected.info;
        }
      }
    } else if (this.builtinInstrument || this.builtinBuffers) {
      this.ensureBuiltinPresets();
      var inst = (this.builtinBuffers && this.builtinBuffers[this.builtinInstrument]) ||
        (this.builtinBuffers && this.builtinBuffers["piano"]);
      if (inst) {
        targetBuffer = inst.buffer;
        basePitch = inst.basePitch;
      }
    }

    if (!targetBuffer) return;

    var src = this.ctx.createBufferSource();
    src.buffer = targetBuffer;

    // 音高重采样率计算：2^((midiNote - basePitch) / 12)
    var pitchDiff = midiNote - basePitch;
    if (sampleInfo && sampleInfo.pitchCorrection) {
      pitchDiff += sampleInfo.pitchCorrection / 100;
    }
    var rate = Math.pow(2, pitchDiff / 12);
    src.playbackRate.setValueAtTime(rate, startTime);

    if (sampleInfo && sampleInfo.endLoop > sampleInfo.startLoop && sampleInfo.endLoop <= sampleInfo.end) {
      src.loop = true;
      src.loopStart = (sampleInfo.startLoop - sampleInfo.start) / sampleInfo.sampleRate;
      src.loopEnd = (sampleInfo.endLoop - sampleInfo.start) / sampleInfo.sampleRate;
    }

    var gain = this.ctx.createGain();
    gain.gain.setValueAtTime(vel * 0.7, startTime);

    src.connect(gain);
    gain.connect(this.masterGain);

    src.start(startTime);

    /* 同音声部入队（此前按音高单槽覆盖：同音重叠时先发的声部被顶掉，
       循环采样更会漏停——和弦/琶音丢声、声部泄漏） */
    var voices = this.activeVoices[midiNote];
    if (!voices) {
      voices = this.activeVoices[midiNote] = [];
    }
    voices.push({
      source: src,
      gain: gain,
      startTime: startTime
    });
  };

  SoundFontPlayer.prototype.noteOff = function (midiNote, when) {
    var voices = this.activeVoices[midiNote];
    if (!voices || !voices.length || !this.ctx) return;

    // FIFO：最早开始的同音声部先结束（符合重复音的音乐直觉）
    var voice = voices.shift();
    if (!voices.length) delete this.activeVoices[midiNote];

    var stopTime = when !== undefined ? when : this.ctx.currentTime;
    var release = 0.2;

    try {
      voice.gain.gain.cancelScheduledValues(stopTime);
      var curGain = Math.max(0.0001, voice.gain.gain.value);
      voice.gain.gain.setValueAtTime(curGain, stopTime);
      voice.gain.gain.exponentialRampToValueAtTime(0.00001, stopTime + release);
      voice.source.stop(stopTime + release + 0.05);

      setTimeout(function () {
        try {
          voice.source.disconnect();
          voice.gain.disconnect();
        } catch (e) {}
      }, (release + 0.1) * 1000);
    } catch (e) {
      try { voice.source.stop(stopTime); } catch (err) {}
    }
  };

  SoundFontPlayer.prototype.stopAll = function () {
    var self = this;
    Object.keys(this.activeVoices).forEach(function (note) {
      var voices = self.activeVoices[note];
      for (var i = 0; i < voices.length; i++) {
        (function (voice) {
          try {
            voice.gain.gain.cancelScheduledValues(self.ctx.currentTime);
            voice.gain.gain.setValueAtTime(Math.max(0.0001, voice.gain.gain.value), self.ctx.currentTime);
            voice.gain.gain.exponentialRampToValueAtTime(0.00001, self.ctx.currentTime + 0.05);
            voice.source.stop(self.ctx.currentTime + 0.1);
            setTimeout(function () {
              try { voice.source.disconnect(); voice.gain.disconnect(); } catch (e) {}
            }, 150);
          } catch (e) {
            try { voice.source.stop(self.ctx.currentTime); } catch (err) {}
          }
        })(voices[i]);
      }
      delete self.activeVoices[note];
    });
    this.activeVoices = {};
  };

  window.SoundFontPlayer = SoundFontPlayer;
})(window);
