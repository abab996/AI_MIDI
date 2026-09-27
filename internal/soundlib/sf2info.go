package soundlib

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
)

// SF2 文件的最小元数据读取：只走 RIFF 块头，不加载采样数据。
// 设置页要显示"31MB · 128 预设"、音源选择器要列出预设名，都得知道
// pdta/phdr 的内容；而音色库动辄上百 MB，整份读入既慢又占内存，
// 因此只 seek 到需要的块。
//
// 结构（SoundFont 2.01 spec）：
//
//	RIFF <size> sfbk
//	  LIST <size> INFO   INAM = 音色库名称
//	  LIST <size> pdta   PHDR = 预设头记录数组，每记录 38 字节，末条为 EOP

const (
	// sf2PhdrRecordSize PHDR 记录定长 38 字节
	sf2PhdrRecordSize = 38
	// sf2MaxPresets 读取上限：畸形/损坏文件声明的记录数可能极大，
	// 这里封顶避免为坏文件分配大块内存
	sf2MaxPresets = 65536
	// sf2MaxTextLen INFO 文本字段上限（INAM 规范为 256，留余量）
	sf2MaxTextLen = 1024
)

var errNotSF2 = errors.New("不是有效的 SF2 文件（缺少 RIFF/sfbk 头）")

// Preset SF2 预设。ID 为 "bank:program"，与机架/卷帘里持久化的
// presetId 同形（旧格式 sf2_<bank>_<program>[_idx] 由前端兼容解析）。
type Preset struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Bank    int    `json:"bank"`
	Program int    `json:"program"`
}

// SF2Info 音色库元数据
type SF2Info struct {
	Name        string   `json:"name,omitempty"`
	PresetCount int      `json:"presetCount"`
	Presets     []Preset `json:"presets,omitempty"`
}

// SF2PresetName 按 presetId（"bank:program"）取预设名，取不到返回空串。
// 机架条目取名要用它（"GeneralUser GS › 钢琴"），而只读 PHDR 块的代价与
// 注册表列举同级，不会因上百 MB 的音色库拖慢 AI 入架。
func SF2PresetName(sourceID, presetID string) string {
	bank, program, ok := ParsePresetID(presetID)
	if !ok {
		return ""
	}
	view, err := Get(sourceID)
	if err != nil || view.Kind != KindSF2 || view.Runtime.Missing || view.Runtime.AbsPath == "" {
		return ""
	}
	info, err := ReadSF2Presets(view.Runtime.AbsPath)
	if err != nil {
		return ""
	}
	for _, p := range info.Presets {
		if p.Bank == bank && p.Program == program {
			return p.Name
		}
	}
	return ""
}

// ParsePresetID 解析预设 id。注册表格式 "0:48" 为主；同时兼容历史
// IndexedDB 格式 "sf2_0_48[_idx]"（老工程的 presetId 直接存进了机架）。
func ParsePresetID(presetID string) (bank, program int, ok bool) {
	id := strings.TrimSpace(presetID)
	if id == "" {
		return 0, 0, false
	}
	if i := strings.Index(id, ":"); i > 0 {
		b, err1 := strconv.Atoi(strings.TrimSpace(id[:i]))
		p, err2 := strconv.Atoi(strings.TrimSpace(id[i+1:]))
		if err1 == nil && err2 == nil && b >= 0 && p >= 0 {
			return b, p, true
		}
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(id, "sf2_"), "_")
	if len(parts) < 2 {
		return 0, 0, false
	}
	b, err1 := strconv.Atoi(parts[0])
	p, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || b < 0 || p < 0 {
		return 0, 0, false
	}
	return b, p, true
}

// ReadSF2Info 读取音色库名称与预设总数（读 PHDR 块头即可，不解逐条记录——
// 设置页列 269 个音色库时不必为每家读一遍预设名）
func ReadSF2Info(path string) (SF2Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return SF2Info{}, err
	}
	defer f.Close()
	return parseSF2Info(f, false)
}

// ReadSF2Presets 读取完整预设列表（名称/库号/音色号）
func ReadSF2Presets(path string) (SF2Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return SF2Info{}, err
	}
	defer f.Close()
	return parseSF2Info(f, true)
}

// chunk 一个 RIFF 块的位置与长度（dataOff 指向块数据起点）
type chunk struct {
	id      string
	dataOff int64
	size    uint32
}

func parseSF2Info(r io.ReadSeeker, withPresets bool) (SF2Info, error) {
	var out SF2Info
	size, err := fileSize(r)
	if err != nil {
		return out, err
	}
	hdr := make([]byte, 12)
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return out, err
	}
	if _, err := io.ReadFull(r, hdr); err != nil {
		return out, err
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "sfbk" {
		return out, errNotSF2
	}
	riffSize := int64(le32(hdr[4:8]))
	end := 8 + riffSize
	if riffSize <= 0 || end > size {
		end = size // 声明长度越界（截断文件）：以实际文件长度为准
	}

	// 只关心两级结构：顶层块 → LIST(INFO/pdta) 的子块。
	// 块 id 大小写：SoundFont 2.01 规范里 pdta 的子块是小写（phdr/pbag/…），
	// 而 LIST 类型名是大写（INFO/sdta/pdta）；不同写库工具混用两种写法，
	// 因此一律按不区分大小写匹配（此前只认大写 "PHDR"，真实音色库全部
	// 解析出 0 个预设——设置页/机架/卷帘的预设列表全空）。
	_ = walkChunks(r, 12, end, func(c chunk) error {
		if c.id != "LIST" || c.size < 4 {
			return nil
		}
		lt := make([]byte, 4)
		if _, err := r.Seek(c.dataOff, io.SeekStart); err != nil {
			return err
		}
		if _, err := io.ReadFull(r, lt); err != nil {
			return err
		}
		subEnd := c.dataOff + int64(c.size)
		switch string(lt) {
		case "pdta":
			return walkChunks(r, c.dataOff+4, subEnd, func(sc chunk) error {
				if !strings.EqualFold(sc.id, "phdr") {
					return nil
				}
				return readPhdr(r, sc, &out, withPresets)
			})
		case "INFO":
			return walkChunks(r, c.dataOff+4, subEnd, func(sc chunk) error {
				if !strings.EqualFold(sc.id, "INAM") {
					return nil
				}
				if name := readText(r, sc); name != "" {
					out.Name = name
				}
				return nil
			})
		}
		return nil
	})
	return out, nil
}

// readPhdr 解析预设头记录数组：末条为 EOP 终止记录，不计入数量。
// withPresets 为 false 时只取数量（数量由块长直接得出，无需读记录）。
func readPhdr(r io.ReadSeeker, c chunk, out *SF2Info, withPresets bool) error {
	count := int(c.size) / sf2PhdrRecordSize
	if count <= 0 {
		return nil
	}
	count-- // EOP
	if count > sf2MaxPresets {
		count = sf2MaxPresets
	}
	out.PresetCount = count
	if !withPresets {
		return nil
	}
	out.Presets = make([]Preset, 0, count)

	buf := make([]byte, sf2PhdrRecordSize)
	for i := 0; i < count; i++ {
		if _, err := r.Seek(c.dataOff+int64(i*sf2PhdrRecordSize), io.SeekStart); err != nil {
			return err
		}
		if _, err := io.ReadFull(r, buf); err != nil {
			// 文件尾被截断：保留已读到的预设
			out.Presets = out.Presets[:i]
			return nil
		}
		bank := int(binary.LittleEndian.Uint16(buf[22:24]))
		program := int(binary.LittleEndian.Uint16(buf[20:22]))
		out.Presets = append(out.Presets, Preset{
			ID:      strconv.Itoa(bank) + ":" + strconv.Itoa(program),
			Name:    cstr(buf[0:20]),
			Bank:    bank,
			Program: program,
		})
	}
	return nil
}

// walkChunks 遍历 [start, end) 内的 RIFF 块；块长越界即停（截断文件）
func walkChunks(r io.ReadSeeker, start, end int64, fn func(c chunk) error) error {
	pos := start
	hdr := make([]byte, 8)
	for pos+8 <= end {
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return err
		}
		if _, err := io.ReadFull(r, hdr); err != nil {
			return nil
		}
		size := le32(hdr[4:8])
		dataOff := pos + 8
		if int64(size) > end-dataOff {
			return nil
		}
		if err := fn(chunk{id: string(hdr[0:4]), dataOff: dataOff, size: size}); err != nil {
			return err
		}
		pos = dataOff + int64(size)
		if size%2 == 1 {
			pos++ // RIFF 奇数长度补齐
		}
	}
	return nil
}

// readText 读取定长文本块（NUL 结尾/填充）
func readText(r io.ReadSeeker, c chunk) string {
	n := int(c.size)
	if n <= 0 {
		return ""
	}
	if n > sf2MaxTextLen {
		n = sf2MaxTextLen
	}
	buf := make([]byte, n)
	if _, err := r.Seek(c.dataOff, io.SeekStart); err != nil {
		return ""
	}
	if _, err := io.ReadFull(r, buf); err != nil {
		return ""
	}
	return cstr(buf)
}

// cstr 截断到第一个 NUL 并去除首尾空白
func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }

func fileSize(r io.ReadSeeker) (int64, error) {
	cur, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	if _, err := r.Seek(cur, io.SeekStart); err != nil {
		return 0, err
	}
	return end, nil
}
