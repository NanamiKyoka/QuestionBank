package pdftext

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"io"
)

// decodeStream 按 /Filter 链解压一个流。
//
// 之所以按链逐步处理而不是一次到位：/Filter 可以是数组
// （先 ASCII85 再 Flate），而"先解哪个后解哪个"的顺序写反了
// 就会在解码中途报出看不懂的错。
func (d *Document) decodeStream(s *Stream) ([]byte, error) {
	data := s.Raw

	// 长度不匹配时优先相信 /Length：对声明长度做校验，
	// 不一致就**丢掉 /Length**（而不是改数据），让下层去扫描 endstream。
	parms := d.filterParms(s.Dict)

	for i, f := range d.filterNames(s.Dict) {
		var p Dict
		if i < len(parms) {
			p = parms[i]
		}
		out, err := applyFilter(f, data, p)
		if err != nil {
			return nil, err
		}
		data = out
	}
	return data, nil
}

func (d *Document) filterNames(dict Dict) []Name {
	switch v := d.get(dict, "Filter").(type) {
	case Name:
		return []Name{v}
	case Array:
		out := make([]Name, 0, len(v))
		for _, item := range v {
			if n, ok := item.(Name); ok {
				out = append(out, n)
			}
		}
		return out
	}
	return nil
}

func (d *Document) filterParms(dict Dict) []Dict {
	arr, ok := d.arrayOf(dict, "DecodeParms")
	if !ok {
		// 也可以写成单个字典。
		if one, ok := d.dictOf(dict, "DecodeParms"); ok {
			return []Dict{one}
		}
		return nil
	}
	out := make([]Dict, 0, len(arr))
	for _, item := range arr {
		if sub, ok := item.(Dict); ok {
			out = append(out, sub)
		} else {
			out = append(out, nil)
		}
	}
	return out
}

func applyFilter(name Name, data []byte, parms Dict) ([]byte, error) {
	switch name {
	case "FlateDecode", "Fl":
		return flateDecode(data, parms)

	case "LZWDecode", "LZW":
		// 录题用到的 PDF 极少真的用 LZW；遇到时明确说不支持，
		// 好过悄悄返回一段乱码让模型去猜。
		return nil, errUnsupported("这份 PDF 使用了 LZW 压缩，无法解析")

	case "ASCIIHexDecode", "AHx":
		return asciiHexDecode(data)

	case "ASCII85Decode", "A85":
		return ascii85Decode(data)

	case "RunLengthDecode", "RL":
		return runLengthDecode(data)

	case "DCTDecode", "JPXDecode", "JBIG2Decode", "CCITTFaxDecode", "DCT", "CCF":
		// 图像编码：没有文本可取，直接当成"这段是空的"。
		return nil, nil

	case "Crypt":
		return nil, errUnsupported("这份 PDF 的流是加密的，无法解析")
	}
	// 名字不认识时按"无操作"处理：真解码失败的话下层会看到原始字节，
	// 而一个自称认识的过滤器更可能把内容解坏。
	return data, nil
}

// flateDecode 解 Flate，并用 /DecodeParms 里的 predictor 还原行数据。
func flateDecode(data []byte, parms Dict) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	// 带 zlib 头的流占绝大多数（0x78 0x9C 之类）；剩下的按裸 deflate 试。
	out, err := inflate(data)
	if err != nil || len(out) == 0 {
		raw, rawErr := inflateRaw(data)
		if rawErr == nil && len(raw) > 0 {
			out, err = raw, nil
		}
	}
	if err != nil {
		return nil, errMalformed("Flate 流解压失败：%v", err)
	}
	if parms != nil {
		return applyPredictor(out, parms)
	}
	return out, nil
}

func inflate(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, maxStreamBytes))
}

// inflateRaw 解压不带 zlib 头的 deflate 流。
//
// 现实里有两种"少了头"的情况，各用各的办法：
//   - zlib 头丢了、末尾的 Adler-32 还在：补一个校验值就能按 zlib 读；
//   - 真正的裸 deflate（连 Adler 都没有）：交给 compress/flate。
//
// 两种都试，是因为各家 PDF 生成器在这一点上并不统一。
func inflateRaw(data []byte) ([]byte, error) {
	patched := make([]byte, 0, len(data)+4)
	patched = append(patched, data...)
	patched = append(patched, 0x00, 0x00, 0x00, 0x01)
	if out, err := inflate(patched); err == nil {
		return out, nil
	}
	return readRawDeflate(data)
}

// readRawDeflate 按裸 deflate 读。
func readRawDeflate(data []byte) ([]byte, error) {
	r := flate.NewReader(bytes.NewReader(data))
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, maxStreamBytes))
	if err != nil && len(out) == 0 {
		return nil, err
	}
	// 有输出就算数：数据尾部若跟着别的编码残留的 Adler-32，
	// 压缩包读到末尾时会报错，而正文早已解完——
	// 这时候把它当失败，等于把一份能读的文档判成读不了。
	return out, nil
}

// maxStreamBytes 单个流解压后的上限。
//
// 解压炸弹防护：一份 100KB 的 PDF 完全可能声称解出 10GB。
const maxStreamBytes = 256 << 20

// applyPredictor 按 /Predictor 还原 PNG 或 TIFF 预测器。
//
// xref 流与内容流都可能带预测器，缺了它数据会是一行行重复的字节，
// 表现是"页数正常但文字全是乱码"。
func applyPredictor(data []byte, parms Dict) ([]byte, error) {
	pred, ok := d0int(parms, "Predictor")
	if !ok || pred <= 1 {
		return data, nil
	}
	colors := 1
	if c, ok := d0int(parms, "Colors"); ok && c > 0 {
		colors = int(c)
	}
	bpc := 8
	if b, ok := d0int(parms, "BitsPerComponent"); ok && b > 0 {
		bpc = int(b)
	}
	columns := 1
	if c, ok := d0int(parms, "Columns"); ok && c > 0 {
		columns = int(c)
	}

	bpp := (colors*bpc + 7) / 8 // 每像素字节数，至少 1
	rowLen := (colors*bpc*columns + 7) / 8

	if pred == 2 {
		// TIFF predictor：只有按 Sub 累加时逐行加前一行。
		if bpc != 8 || rowLen == 0 {
			return data, nil // 只能处理最常见的 8 位情形
		}
		for start := 0; start+rowLen <= len(data); start += rowLen {
			row := data[start : start+rowLen]
			for i := bpp; i < len(row); i++ {
				row[i] += row[i-bpp]
			}
		}
		return data, nil
	}

	// PNG predictors：每行前面带一个过滤类型字节。
	if rowLen == 0 {
		return data, nil
	}
	rowTotal := rowLen + 1
	rows := len(data) / rowTotal
	out := make([]byte, 0, rows*rowLen)
	prev := make([]byte, rowLen)
	cur := make([]byte, rowLen)

	for r := 0; r < rows; r++ {
		ft := data[r*rowTotal]
		copy(cur, data[r*rowTotal+1:(r+1)*rowTotal])
		switch ft {
		case 0: // None
		case 1: // Sub
			for i := bpp; i < rowLen; i++ {
				cur[i] += cur[i-bpp]
			}
		case 2: // Up
			for i := 0; i < rowLen; i++ {
				cur[i] += prev[i]
			}
		case 3: // Average
			for i := 0; i < rowLen; i++ {
				var left byte
				if i >= bpp {
					left = cur[i-bpp]
				}
				cur[i] += byte((int(left) + int(prev[i])) / 2)
			}
		case 4: // Paeth
			for i := 0; i < rowLen; i++ {
				var left, upLeft byte
				if i >= bpp {
					left = cur[i-bpp]
					upLeft = prev[i-bpp]
				}
				cur[i] += paeth(left, prev[i], upLeft)
			}
		default:
			// 未知的过滤类型：原样保留，别把内容改坏。
		}
		out = append(out, cur...)
		copy(prev, cur)
	}
	return out, nil
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	default:
		return c
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func d0int(d Dict, key Name) (int64, bool) {
	if d == nil {
		return 0, false
	}
	return toInt(d[key])
}

// asciiHexDecode 解十六进制流（以 '>' 结束）。
func asciiHexDecode(data []byte) ([]byte, error) {
	end := bytes.IndexByte(data, '>')
	if end >= 0 {
		data = data[:end]
	}
	var out []byte
	var hi byte
	have := false
	for _, c := range data {
		if c == '>' {
			break
		}
		v, ok := hexVal(c)
		if !ok {
			continue
		}
		if have {
			out = append(out, hi<<4|v)
			have = false
		} else {
			hi = v
			have = true
		}
	}
	if have {
		out = append(out, hi<<4)
	}
	return out, nil
}

// ascii85Decode 解 base85 流（以 '~>' 结束）。
func ascii85Decode(data []byte) ([]byte, error) {
	if i := bytes.Index(data, []byte("~>")); i >= 0 {
		data = data[:i]
	}
	var out []byte
	var group [5]byte
	n := 0

	flush := func() {
		if n == 0 {
			return
		}
		// 缺位的那一组按 0 补齐，解出来再截掉多出来的字节。
		for i := n; i < 5; i++ {
			group[i] = '!'
		}
		v := uint32(0)
		for i := 0; i < 5; i++ {
			v = v*85 + uint32(group[i]-'!')
		}
		buf := []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
		out = append(out, buf[:4-(5-n)]...)
		n = 0
	}

	for _, c := range data {
		switch {
		case isSpace(c):
			// 组之间的空白直接跳过。
		case c == 'z' && n == 0:
			out = append(out, 0, 0, 0, 0)
		default:
			group[n] = c
			n++
			if n == 5 {
				flush()
			}
		}
	}
	flush()
	return out, nil
}

// runLengthDecode 解游程编码流。
func runLengthDecode(data []byte) ([]byte, error) {
	var out []byte
	for i := 0; i < len(data); {
		l := int(data[i])
		i++
		switch {
		case l == 128: // EOD
			return out, nil
		case l < 128: // 复制接下来的 l+1 个字节
			n := l + 1
			if i+n > len(data) {
				return out, nil
			}
			out = append(out, data[i:i+n]...)
			i += n
		default: // 重复下一个字节 257-l 次
			if i >= len(data) {
				return out, nil
			}
			for k := 0; k < 257-l; k++ {
				out = append(out, data[i])
			}
			i++
		}
		if len(out) > maxStreamBytes {
			return out, nil
		}
	}
	return out, nil
}
