/* 标准 MIDI 文件 (SMF) 前端解析器 —— 钢琴卷帘与编排窗口共用 */
(function (window) {
  "use strict";

  var NOTE_NAMES = ["C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"];

  function numberToNoteName(num) {
    num = Math.max(0, Math.min(127, Math.round(num)));
    var oct = Math.floor(num / 12) - 1;
    return NOTE_NAMES[num % 12] + oct;
  }

  /**
   * 解析 SMF 字节为扁平音符数组
   * @param {ArrayBuffer} arrayBuffer
   * @returns {Array<{note:string, velocity:number, start:number, end:number}>}
   *          start/end 单位为拍（基于 MIDI 文件的 ticksPerBeat），保留 3 位小数
   */
  function parseBytes(arrayBuffer) {
    var notes = [];
    var view = new DataView(arrayBuffer);
    if (!arrayBuffer || view.byteLength < 14) return notes;

    try {
      // RIFF 包裹的 RMID 格式：跳过 "RIFFxxxxRMID" 头定位到 MThd
      var offset = 0;
      if (view.getUint16(0) === 0x5249) {
        var scan = new Uint8Array(arrayBuffer, 0, Math.min(view.byteLength, 512));
        for (var s = 0; s < scan.length - 4; s++) {
          if (scan[s] === 0x4d && scan[s + 1] === 0x54 && scan[s + 2] === 0x68 && scan[s + 3] === 0x64) {
            offset = s;
            break;
          }
        }
      }

      var header = offset;
      if (view.getUint16(header + 0) !== 0x4d54) return notes; // 无 MThd
      var division = view.getInt16(header + 12);   // 有符号：最高位为 1 表示 SMPTE 时间格式
      var ticksPerBeat = 480;
      if (division > 0) {
        ticksPerBeat = division;                   // 常见 PPQ 格式
      } else if (division < 0) {
        // SMPTE：高字节负号帧率 fps（取绝对值），低字节每帧 tick 数
        var fps = Math.abs(division >> 8) || 30;
        var tpf = division & 0xFF || 4;
        // 换算为 120 BPM 等价 PPQ（秒 → 拍），保证时间轴不塌缩到 0
        ticksPerBeat = Math.max(24, Math.round(fps * tpf * 0.5));
      }

      var pos = header + 14;

      while (pos + 8 <= view.byteLength) {
        var chunkType = String.fromCharCode(
          view.getUint8(pos), view.getUint8(pos + 1), view.getUint8(pos + 2), view.getUint8(pos + 3)
        );
        var chunkLen = view.getUint32(pos + 4);
        pos += 8;

        if (chunkType === "MTrk") {
          var trackEnd = pos + chunkLen;
          var runningStatus = 0;
          var currentTick = 0;
          /* note-on/off 配对表必须每条轨道独立（此前跨轨共享：
             多轨 MIDI 同音高互相错配 → 音符长度错乱、大量重叠长音） */
          var activeMap = {};

          while (pos < trackEnd) {
            var delta = 0;
            var b = 0;
            do {
              b = view.getUint8(pos++);
              delta = (delta << 7) | (b & 0x7f);
            } while (b & 0x80);
            currentTick += delta;

            var status = view.getUint8(pos);
            if (status & 0x80) {
              runningStatus = status;
              pos++;
            } else {
              status = runningStatus;
            }

            var type = status & 0xf0;
            if (type === 0x90) {
              var p = view.getUint8(pos++);
              var v = view.getUint8(pos++);
              var beat = currentTick / ticksPerBeat;
              /* 通道+音高联合键控：不同通道的同音高是不同的音符 */
              var key = ((status & 0x0f) << 7) | p;
              if (v > 0) {
                activeMap[key] = { start: beat, vel: v };
              } else if (activeMap[key]) {
                var st = activeMap[key];
                notes.push({
                  note: numberToNoteName(p),
                  velocity: st.vel,
                  start: Math.round(st.start * 1000) / 1000,
                  end: Math.round(beat * 1000) / 1000
                });
                delete activeMap[key];
              }
            } else if (type === 0x80) {
              var p8 = view.getUint8(pos++);
              pos++;
              var beat8 = currentTick / ticksPerBeat;
              var key8 = ((status & 0x0f) << 7) | p8;
              if (activeMap[key8]) {
                var st8 = activeMap[key8];
                notes.push({
                  note: numberToNoteName(p8),
                  velocity: st8.vel,
                  start: Math.round(st8.start * 1000) / 1000,
                  end: Math.round(beat8 * 1000) / 1000
                });
                delete activeMap[key8];
              }
            } else if (type === 0xc0 || type === 0xd0) {
              pos++;
            } else if (type === 0xa0 || type === 0xb0 || type === 0xe0) {
              pos += 2;
            } else if (status === 0xff) {
              pos++;
              var metaLen = 0;
              do {
                b = view.getUint8(pos++);
                metaLen = (metaLen << 7) | (b & 0x7f);
              } while (b & 0x80);
              pos += metaLen;
            } else if (status === 0xf0 || status === 0xf7) {
              /* 系统专用事件：按长度跳过（此前直接 break——轨道中段的
                 sysex 会丢弃其后全部音符，造成大面积错位） */
              pos++;
              var syxLen = 0;
              do {
                b = view.getUint8(pos++);
                syxLen = (syxLen << 7) | (b & 0x7f);
              } while (b & 0x80);
              pos += syxLen;
            } else if (status >= 0xf0) {
              break;
            }
          }
        } else {
          pos += chunkLen;
        }
      }
    } catch (e) {
      console.warn("MIDI parse warning:", e);
    }
    return notes;
  }

  window.MidiParse = {
    parseBytes: parseBytes,
    numberToNoteName: numberToNoteName,
    noteNameToNumber: function (name) {
      if (!name) return 60;
      var m = /^([A-Ga-g][#b]?)(-?\d+)$/.exec(String(name).trim());
      if (!m) return 60;
      var p = m[1].toUpperCase();
      var oct = parseInt(m[2], 10);
      var map = { "C": 0, "C#": 1, "DB": 1, "D": 2, "D#": 3, "EB": 3, "E": 4, "F": 5, "F#": 6, "GB": 6, "G": 7, "G#": 8, "AB": 8, "A": 9, "A#": 10, "BB": 10, "B": 11 };
      var pitch = map[p] !== undefined ? map[p] : 0;
      return (oct + 1) * 12 + pitch;
    }
  };
})(window);
