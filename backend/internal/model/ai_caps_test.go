package model

import "testing"

// 集合解析必须归一化：同一组能力不管从哪个入口、按什么顺序进来，
// 存下来的字符串都得一样。否则"改了一下点选顺序"就会让整行数据变化，
// 审计与排查都无从谈起。
func TestParseAIModelCapsNormalizesOrder(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"text", "text"},
		{"text,vision", "text,vision"},
		{"vision,text", "text,vision"},
		{"embedding,vision,text", "text,vision,embedding"},
		{" text , vision ", "text,vision"},
		{"text,text,text", "text"},
		{"", ""},
		{",", ""},
	}
	for _, c := range cases {
		if got := ParseAIModelCaps(c.in).String(); got != c.want {
			t.Errorf("ParseAIModelCaps(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 读既有数据时遇到不认识的值只能丢掉，不能让整个设置页打不开。
func TestParseAIModelCapsDropsUnknownValues(t *testing.T) {
	got := ParseAIModelCaps("text,audio,vision")
	if got.String() != "text,vision" {
		t.Fatalf("未知用途应被丢弃，实际 %q", got.String())
	}
	if !got.Has(AIModelText) || !got.Has(AIModelVision) || got.Has(AIModelEmbedding) {
		t.Fatalf("能力判定不对: %v", got)
	}
}

// 写入路径必须严格：空集合与未知取值都要报错。
func TestNormalizeAIModelCapsRejectsBadInput(t *testing.T) {
	for _, in := range [][]string{nil, {}, {"", "  "}, {"audio"}} {
		if _, err := NormalizeAIModelCaps(in); err == nil {
			t.Errorf("NormalizeAIModelCaps(%v) 应当报错", in)
		}
	}
	got, err := NormalizeAIModelCaps([]string{"vision", "text", "text"})
	if err != nil {
		t.Fatalf("合法输入不该报错: %v", err)
	}
	if got.String() != "text,vision" {
		t.Fatalf("去重与排序不对: %q", got.String())
	}
}

// Labels/Values 是响应里直接用的形态，必须与集合一一对应。
func TestAIModelCapsLabelsMatchValues(t *testing.T) {
	caps := ParseAIModelCaps("text,vision,embedding")
	values := caps.Values()
	labels := caps.Labels()
	if len(values) != 3 || len(labels) != 3 {
		t.Fatalf("长度不对: %v %v", values, labels)
	}
	for i, v := range values {
		if string(AIModelType(v)) != string(caps[i]) {
			t.Fatalf("第 %d 项对不上: %q vs %q", i, v, caps[i])
		}
	}
	if labels[0] != "文本" || labels[1] != "视觉" || labels[2] != "向量" {
		t.Fatalf("中文名不对: %v", labels)
	}
}
