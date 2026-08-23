/* FL Studio 风格高级辅助工具箱与乐理算法 */
(function (window) {
  "use strict";

  var SCALES = {
    "none": { name: "无 (None)", intervals: [] },
    "major": { name: "自然大调 (Major)", intervals: [0, 2, 4, 5, 7, 9, 11] },
    "minor": { name: "自然小调 (Natural Minor)", intervals: [0, 2, 3, 5, 7, 8, 10] },
    "harmonic_minor": { name: "和声小调 (Harmonic Minor)", intervals: [0, 2, 3, 5, 7, 8, 11] },
    "melodic_minor": { name: "旋律小调 (Melodic Minor)", intervals: [0, 2, 3, 5, 7, 9, 11] },
    "dorian": { name: "多利亚 (Dorian)", intervals: [0, 2, 3, 5, 7, 9, 10] },
    "phrygian": { name: "弗里几亚 (Phrygian)", intervals: [0, 1, 3, 5, 7, 8, 10] },
    "lydian": { name: "利底亚 (Lydian)", intervals: [0, 2, 4, 6, 7, 9, 11] },
    "mixolydian": { name: "混合利底亚 (Mixolydian)", intervals: [0, 2, 4, 5, 7, 9, 10] },
    "locrian": { name: "洛克里亚 (Locrian)", intervals: [0, 1, 3, 5, 6, 8, 10] },
    "pentatonic_major": { name: "五声大调 (Pentatonic Major)", intervals: [0, 2, 4, 7, 9] },
    "pentatonic_minor": { name: "五声小调 (Pentatonic Minor)", intervals: [0, 3, 5, 7, 10] },
    "blues": { name: "布鲁斯 (Blues)", intervals: [0, 3, 5, 6, 7, 10] },
    "whole_tone": { name: "全音阶 (Whole Tone)", intervals: [0, 2, 4, 6, 8, 10] }
  };

  var CHORDS = {
    "maj": { name: "大三和弦 (Major)", intervals: [0, 4, 7] },
    "min": { name: "小三和弦 (Minor)", intervals: [0, 3, 7] },
    "dim": { name: "减三和弦 (Diminished)", intervals: [0, 3, 6] },
    "aug": { name: "增三和弦 (Augmented)", intervals: [0, 4, 8] },
    "sus2": { name: "挂二和弦 (Sus2)", intervals: [0, 2, 7] },
    "sus4": { name: "挂四和弦 (Sus4)", intervals: [0, 5, 7] },
    "maj7": { name: "大大七和弦 (Maj7)", intervals: [0, 4, 7, 11] },
    "min7": { name: "小小七和弦 (Min7)", intervals: [0, 3, 7, 10] },
    "dom7": { name: "属七和弦 (7 / Dom7)", intervals: [0, 4, 7, 10] },
    "dim7": { name: "减七和弦 (Dim7)", intervals: [0, 3, 6, 9] },
    "half_dim7": { name: "半减七和弦 (m7b5)", intervals: [0, 3, 6, 10] },
    "min_maj7": { name: "小大七和弦 (mMaj7)", intervals: [0, 3, 7, 11] },
    "add9": { name: "加九和弦 (Add9)", intervals: [0, 4, 7, 14] },
    "maj9": { name: "大九和弦 (Maj9)", intervals: [0, 4, 7, 11, 14] },
    "min9": { name: "小九和弦 (Min9)", intervals: [0, 3, 7, 10, 14] }
  };

  var NOTE_NAMES = ["C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"];

  function noteNameToNumber(name) {
    if (!name) return 60;
    var m = /^([A-Ga-g][#b]?)(-?\d+)$/.exec(name.trim());
    if (!m) return 60;
    var p = m[1].toUpperCase();
    var oct = parseInt(m[2], 10);
    var map = { "C": 0, "C#": 1, "DB": 1, "D": 2, "D#": 3, "EB": 3, "E": 4, "F": 5, "F#": 6, "GB": 6, "G": 7, "G#": 8, "AB": 8, "A": 9, "A#": 10, "BB": 10, "B": 11 };
    var pitch = map[p] !== undefined ? map[p] : 0;
    return (oct + 1) * 12 + pitch;
  }

  function numberToNoteName(num) {
    num = Math.max(0, Math.min(127, Math.round(num)));
    var oct = Math.floor(num / 12) - 1;
    var pitch = NOTE_NAMES[num % 12];
    return pitch + oct;
  }

  var PianoRollTools = {
    SCALES: SCALES,
    CHORDS: CHORDS,
    NOTE_NAMES: NOTE_NAMES,
    noteNameToNumber: noteNameToNumber,
    numberToNoteName: numberToNoteName,

    isNoteInScale: function (midiNote, rootPitch, scaleKey) {
      if (!scaleKey || scaleKey === "none" || !SCALES[scaleKey]) return true;
      var intervals = SCALES[scaleKey].intervals;
      if (!intervals || !intervals.length) return true;
      var rel = (midiNote - rootPitch + 1200) % 12;
      return intervals.indexOf(rel) !== -1;
    },

    stampChord: function (rootMidiNote, chordTypeKey, startBeat, durationBeat, velocity) {
      var chord = CHORDS[chordTypeKey] || CHORDS["maj"];
      var notes = [];
      var dur = durationBeat || 1.0;
      var vel = velocity || 100;
      chord.intervals.forEach(function (iv) {
        var p = Math.max(0, Math.min(127, rootMidiNote + iv));
        notes.push({
          note: numberToNoteName(p),
          velocity: vel,
          start: startBeat,
          end: startBeat + dur
        });
      });
      return notes;
    },

    // 扫弦算法 (Strum)
    strum: function (notes, timeOffsetBeat, velRamp, alternateDir) {
      if (!notes || notes.length <= 1) return notes;
      var dt = timeOffsetBeat !== undefined ? timeOffsetBeat : 0.04;
      var ramp = velRamp !== undefined ? velRamp : -8;

      var groups = [];
      var sorted = notes.slice().sort(function (a, b) {
        return a.start - b.start || noteNameToNumber(a.note) - noteNameToNumber(b.note);
      });

      var curGroup = [sorted[0]];
      for (var i = 1; i < sorted.length; i++) {
        if (Math.abs(sorted[i].start - curGroup[0].start) < 0.1) {
          curGroup.push(sorted[i]);
        } else {
          groups.push(curGroup);
          curGroup = [sorted[i]];
        }
      }
      if (curGroup.length) groups.push(curGroup);

      var result = [];
      groups.forEach(function (grp, gIdx) {
        var up = alternateDir ? (gIdx % 2 === 0) : true;
        grp.sort(function (a, b) {
          var pa = noteNameToNumber(a.note);
          var pb = noteNameToNumber(b.note);
          return up ? (pa - pb) : (pb - pa);
        });
        grp.forEach(function (n, idx) {
          var clone = Object.assign({}, n);
          clone.start = Math.round((clone.start + idx * dt) * 1000) / 1000;
          clone.velocity = Math.max(1, Math.min(127, clone.velocity + idx * ramp));
          result.push(clone);
        });
      });
      return result;
    },

    // 琶音器 (Arpeggiator)
    arpeggiate: function (notes, pattern, stepBeat, gate) {
      if (!notes || !notes.length) return notes;
      var step = stepBeat || 0.25;
      var pat = pattern || "up";
      var g = gate || 0.9;

      var pitches = notes.map(function (n) {
        return { pitch: noteNameToNumber(n.note), vel: n.velocity };
      }).sort(function (a, b) { return a.pitch - b.pitch; });

      var minStart = Math.min.apply(null, notes.map(function (n) { return n.start; }));
      var maxEnd = Math.max.apply(null, notes.map(function (n) { return n.end; }));
      var totalSteps = Math.max(1, Math.floor((maxEnd - minStart) / step));

      var seq = [];
      if (pat === "up") {
        for (var i = 0; i < totalSteps; i++) seq.push(pitches[i % pitches.length]);
      } else if (pat === "down") {
        for (var j = 0; j < totalSteps; j++) seq.push(pitches[(pitches.length - 1 - (j % pitches.length))]);
      } else if (pat === "up_down") {
        var cycle = pitches.slice().concat(pitches.slice(1, -1).reverse());
        if (!cycle.length) cycle = pitches;
        for (var k = 0; k < totalSteps; k++) seq.push(cycle[k % cycle.length]);
      } else {
        for (var r = 0; r < totalSteps; r++) seq.push(pitches[Math.floor(Math.random() * pitches.length)]);
      }

      var result = [];
      seq.forEach(function (item, idx) {
        var st = minStart + idx * step;
        result.push({
          note: numberToNoteName(item.pitch),
          velocity: item.vel || 100,
          start: Math.round(st * 1000) / 1000,
          end: Math.round((st + step * g) * 1000) / 1000
        });
      });
      return result;
    },

    // 力度缩放/偏移/渐变 (Level Scaling / Alt+X)
    levelScale: function (notes, multiply, offset, rampStart, rampEnd) {
      if (!notes || !notes.length) return notes;
      var mult = multiply !== undefined ? multiply : 1.0;
      var off = offset !== undefined ? offset : 0;
      var rStart = rampStart !== undefined ? rampStart : 0;
      var rEnd = rampEnd !== undefined ? rampEnd : 0;

      var minStart = Math.min.apply(null, notes.map(function (n) { return n.start; }));
      var maxEnd = Math.max.apply(null, notes.map(function (n) { return n.end; }));
      var span = Math.max(0.1, maxEnd - minStart);

      return notes.map(function (n) {
        var clone = Object.assign({}, n);
        var progress = (clone.start - minStart) / span;
        var ramp = rStart + (rEnd - rStart) * progress;
        var v = (clone.velocity * mult) + off + ramp;
        clone.velocity = Math.max(1, Math.min(127, Math.round(v)));
        return clone;
      });
    },

    // 随机化工具 (Randomizer / Alt+R)
    randomize: function (notes, velRange, pitchRange, timeRange) {
      if (!notes || !notes.length) return notes;
      var vr = velRange || 0;
      var pr = pitchRange || 0;
      var tr = timeRange || 0;

      return notes.map(function (n) {
        var clone = Object.assign({}, n);
        if (vr > 0) {
          var dv = (Math.random() * 2 - 1) * vr;
          clone.velocity = Math.max(1, Math.min(127, Math.round(clone.velocity + dv)));
        }
        if (pr > 0) {
          var dp = Math.round((Math.random() * 2 - 1) * pr);
          var p = Math.max(0, Math.min(127, noteNameToNumber(clone.note) + dp));
          clone.note = numberToNoteName(p);
        }
        if (tr > 0) {
          var dt = (Math.random() * 2 - 1) * tr;
          var dur = clone.end - clone.start;
          clone.start = Math.max(0, Math.round((clone.start + dt) * 1000) / 1000);
          clone.end = Math.round((clone.start + dur) * 1000) / 1000;
        }
        return clone;
      });
    },

    // 旋律翻转 (Flip Score / Alt+Y)
    flip: function (notes, mode) {
      if (!notes || !notes.length) return notes;
      var m = mode || "vertical"; // "vertical" (倒影/音高翻转) or "horizontal" (逆行/时间翻转)

      if (m === "vertical") {
        var pitches = notes.map(function (n) { return noteNameToNumber(n.note); });
        var minP = Math.min.apply(null, pitches);
        var maxP = Math.max.apply(null, pitches);
        var center = (minP + maxP) / 2;
        return notes.map(function (n) {
          var clone = Object.assign({}, n);
          var p = noteNameToNumber(clone.note);
          var flipped = Math.round(center + (center - p));
          clone.note = numberToNoteName(Math.max(0, Math.min(127, flipped)));
          return clone;
        });
      } else {
        var minStart = Math.min.apply(null, notes.map(function (n) { return n.start; }));
        var maxEnd = Math.max.apply(null, notes.map(function (n) { return n.end; }));
        return notes.map(function (n) {
          var clone = Object.assign({}, n);
          var dur = clone.end - clone.start;
          var newStart = maxEnd - (clone.end - minStart);
          clone.start = Math.round(newStart * 1000) / 1000;
          clone.end = Math.round((newStart + dur) * 1000) / 1000;
          return clone;
        });
      }
    },

    // 智能量化 (Quantize)
    quantize: function (notes, gridStepBeat, quantizeEnd, swing) {
      if (!notes || !notes.length) return notes;
      var step = gridStepBeat || 0.25;
      var sw = swing || 0; // 0 to 1
      return notes.map(function (n) {
        var clone = Object.assign({}, n);
        var snappedStart = Math.round(clone.start / step) * step;
        // Swing 摇摆计算 (偶数步施加偏移)
        var stepIdx = Math.round(snappedStart / step);
        if (sw > 0 && stepIdx % 2 === 1) {
          snappedStart += step * sw * 0.33;
        }
        var dur = Math.max(step * 0.5, clone.end - clone.start);
        clone.start = Math.round(snappedStart * 1000) / 1000;
        if (quantizeEnd) {
          var snappedEnd = Math.round(clone.end / step) * step;
          clone.end = Math.max(clone.start + step * 0.5, Math.round(snappedEnd * 1000) / 1000);
        } else {
          clone.end = Math.round((clone.start + dur) * 1000) / 1000;
        }
        return clone;
      });
    },

    // 移调 (Transpose)
    transpose: function (notes, semitones) {
      if (!notes || !notes.length || semitones === 0) return notes;
      return notes.map(function (n) {
        var clone = Object.assign({}, n);
        var p = noteNameToNumber(clone.note);
        var newP = Math.max(0, Math.min(127, p + semitones));
        clone.note = numberToNoteName(newP);
        return clone;
      });
    },

    // 连奏 (Legato)
    legato: function (notes) {
      if (!notes || notes.length <= 1) return notes;
      var sorted = notes.slice().sort(function (a, b) { return a.start - b.start; });
      for (var i = 0; i < sorted.length - 1; i++) {
        if (sorted[i + 1].start > sorted[i].start) {
          sorted[i].end = sorted[i + 1].start;
        }
      }
      return sorted;
    }
  };

  window.PianoRollTools = PianoRollTools;
})(window);
