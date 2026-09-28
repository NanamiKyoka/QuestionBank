package omml

import (
	"strings"
	"testing"
)

func TestRenderBasicRuns(t *testing.T) {
	got := Render("x")
	if !strings.Contains(got, "<m:t xml:space=\"preserve\">x</m:t>") {
		t.Fatalf("应输出变量 x，实际 %s", got)
	}
	// 变量用数学斜体，不应带 sty=p。
	if strings.Contains(got, `m:val="p"`) {
		t.Fatalf("变量不应被标记为正体: %s", got)
	}
}

// 数字必须正体，否则 "2x" 会渲染成两个斜体字符，不符合印刷习惯。
func TestRenderNumbersAreUpright(t *testing.T) {
	got := Render("2x")
	if !strings.Contains(got, `<m:sty m:val="p"/>`) {
		t.Fatalf("数字 2 应为正体，实际 %s", got)
	}
	// 数字与变量必须被拆成两个运行，否则会被统一样式。
	if strings.Count(got, "<m:r>") != 2 {
		t.Fatalf("2x 应拆成两个运行，实际 %s", got)
	}
	if !strings.Contains(got, `<m:t xml:space="preserve">x</m:t>`) {
		t.Fatalf("变量 x 应存在，实际 %s", got)
	}
}

func TestRenderScripts(t *testing.T) {
	cases := []struct {
		latex string
		want  string
	}{
		{"x^2", "<m:sSup>"},
		{"x_1", "<m:sSub>"},
		{"x_1^2", "<m:sSubSup>"},
	}
	for _, tc := range cases {
		got := Render(tc.latex)
		if !strings.Contains(got, tc.want) {
			t.Fatalf("%s 应包含 %s，实际 %s", tc.latex, tc.want, got)
		}
	}
}

func TestRenderFrac(t *testing.T) {
	got := Render(`\frac{a}{b}`)
	for _, want := range []string{"<m:f>", "<m:num>", "<m:den>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("应包含 %s，实际 %s", want, got)
		}
	}
	// 分子分母的内容都要在。
	if !strings.Contains(got, ">a</m:t>") || !strings.Contains(got, ">b</m:t>") {
		t.Fatalf("分子分母内容缺失: %s", got)
	}
}

func TestRenderSqrt(t *testing.T) {
	square := Render(`\sqrt{x}`)
	// 平方根要隐藏次数占位。
	if !strings.Contains(square, `<m:degHide m:val="1"/>`) {
		t.Fatalf("平方根应隐藏次数，实际 %s", square)
	}

	cube := Render(`\sqrt[3]{x}`)
	if strings.Contains(cube, "degHide") {
		t.Fatalf("n 次根不应隐藏次数，实际 %s", cube)
	}
	if !strings.Contains(cube, ">3</m:t>") {
		t.Fatalf("n 次根应包含次数 3，实际 %s", cube)
	}
}

func TestRenderSymbols(t *testing.T) {
	cases := map[string]string{
		`\alpha`:      "α",
		`\beta`:       "β",
		`\times`:      "×",
		`\leq`:        "≤",
		`\infty`:      "∞",
		`\rightarrow`: "→",
		`\pm`:         "±",
	}
	for latex, want := range cases {
		got := Render(latex)
		if !strings.Contains(got, want) {
			t.Fatalf("%s 应渲染为 %s，实际 %s", latex, want, got)
		}
	}
}

// 大写希腊字母在数学排版里是正体。
func TestRenderUppercaseGreekIsUpright(t *testing.T) {
	got := Render(`\Gamma`)
	if !strings.Contains(got, `m:val="p"`) {
		t.Fatalf("大写希腊字母应为正体，实际 %s", got)
	}
}

func TestRenderNary(t *testing.T) {
	got := Render(`\sum_{i=1}^{n} a_i`)
	if !strings.Contains(got, "<m:nary>") {
		t.Fatalf("应输出 nary，实际 %s", got)
	}
	if !strings.Contains(got, `m:val="∑"`) {
		t.Fatalf("应使用求和符号，实际 %s", got)
	}
	if !strings.Contains(got, `m:val="undOvr"`) {
		t.Fatalf("求和的上下限应排在上下方，实际 %s", got)
	}
	// 关键回归：a_i 的下标属于 a，不能被挂到求和符号上。
	if strings.Count(got, "<m:sSub>") != 1 {
		t.Fatalf("应恰有一个独立下标（a_i），实际 %s", got)
	}
}

// 积分的上下限在右侧，与求和的上下方不同。
func TestRenderIntegralLimitsOnSide(t *testing.T) {
	got := Render(`\int_0^1 f(x) dx`)
	if !strings.Contains(got, `m:val="subSup"`) {
		t.Fatalf("积分上下限应排在右侧，实际 %s", got)
	}
}

func TestRenderDelimiters(t *testing.T) {
	got := Render(`\left( \frac{a}{b} \right)`)
	if !strings.Contains(got, "<m:d>") {
		t.Fatalf("应输出定界符节点，实际 %s", got)
	}
	if !strings.Contains(got, `<m:begChr m:val="("/>`) || !strings.Contains(got, `<m:endChr m:val=")"/>`) {
		t.Fatalf("定界符应为圆括号，实际 %s", got)
	}
}

func TestRenderFunction(t *testing.T) {
	got := Render(`\sin x`)
	if !strings.Contains(got, "<m:func>") {
		t.Fatalf("应输出函数节点，实际 %s", got)
	}
	if !strings.Contains(got, ">sin</m:t>") {
		t.Fatalf("应包含函数名 sin，实际 %s", got)
	}

	// 没有参数时不应生成空的函数参数容器。
	bare := Render(`\sin`)
	if strings.Contains(bare, "<m:func>") {
		t.Fatalf("无参数时不应输出函数容器，实际 %s", bare)
	}
}

func TestRenderLimLow(t *testing.T) {
	got := Render(`\lim_{x \to 0} f(x)`)
	if !strings.Contains(got, "<m:limLow>") {
		t.Fatalf("应输出 limLow，实际 %s", got)
	}
	if !strings.Contains(got, ">lim</m:t>") {
		t.Fatalf("应包含 lim，实际 %s", got)
	}
	if !strings.Contains(got, "→") {
		t.Fatalf("条件里的箭头应渲染，实际 %s", got)
	}
}

func TestRenderText(t *testing.T) {
	got := Render(`\text{速度} v`)
	if !strings.Contains(got, "速度") {
		t.Fatalf("文本内容应保留，实际 %s", got)
	}
	// \text 的内容应按正体排版。
	if !strings.Contains(got, `m:val="p"`) {
		t.Fatalf("\\text 内容应为正体，实际 %s", got)
	}
}

func TestRenderAccent(t *testing.T) {
	if got := Render(`\vec{a}`); !strings.Contains(got, "<m:acc>") {
		t.Fatalf("应输出重音节点，实际 %s", got)
	}
	if got := Render(`\hat{x}`); !strings.Contains(got, "<m:acc>") {
		t.Fatalf("应输出重音节点，实际 %s", got)
	}
}

// 未知命令退化为字面文本，而不是让整份导出失败。
func TestRenderUnknownCommandDegrades(t *testing.T) {
	got := Render(`\unknowncmd{x}`)
	if got == "" {
		t.Fatal("不应输出空内容")
	}
	if !strings.Contains(got, "unknowncmd") {
		t.Fatalf("未知命令应原样输出，实际 %s", got)
	}
	if !strings.Contains(got, ">x</m:t>") {
		t.Fatalf("未知命令的参数仍应保留，实际 %s", got)
	}
}

// 括号不配对时也要能恢复，不能丢内容。
func TestRenderMalformedInputRecovers(t *testing.T) {
	cases := []string{
		`\frac{a}{b`, // 缺右括号
		`x^`,         // 缺上标
		`{a}{b}`,     // 多余分组
		`\left( a`,   // 缺 \right
		`\sqrt`,      // 缺参数
		"a}b",        // 多余右括号
	}
	for _, src := range cases {
		got := Render(src)
		if got == "" {
			t.Fatalf("%q 应至少产出可渲染内容", src)
		}
		if !strings.HasPrefix(got, "<m:oMath>") || !strings.HasSuffix(got, "</m:oMath>") {
			t.Fatalf("%q 输出结构不完整: %s", src, got)
		}
	}
}

// 公式里的尖括号与 & 必须转义，否则会破坏 document.xml。
func TestRenderEscapesXML(t *testing.T) {
	got := Render(`a<b \text{&} c`)
	if strings.Contains(got, "<b") && !strings.Contains(got, "&lt;") {
		t.Fatalf("小于号应被转义: %s", got)
	}
	if !strings.Contains(got, "&amp;") {
		t.Fatalf("& 应被转义: %s", got)
	}
}

func TestRenderEmpty(t *testing.T) {
	if got := Render(""); got != "" {
		t.Fatalf("空公式应返回空串，实际 %q", got)
	}
	if got := Render("   "); got != "" {
		t.Fatalf("空白公式应返回空串，实际 %q", got)
	}
}

func TestStripMathDelimiters(t *testing.T) {
	cases := map[string]string{
		"$x$":     "x",
		"$$x$$":   "x",
		`\[x\]`:   "x",
		`\(x\)`:   "x",
		" x ":     "x",
		`$ x^2 $`: "x^2",
		"x+y":     "x+y",
	}
	for in, want := range cases {
		if got := StripMathDelimiters(in); got != want {
			t.Fatalf("StripMathDelimiters(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestIsBalanced(t *testing.T) {
	if !IsBalanced(`\frac{a}{b}`) {
		t.Fatal("配对的括号应判定为平衡")
	}
	if IsBalanced(`\frac{a}{b`) {
		t.Fatal("缺右括号应判定为不平衡")
	}
	if IsBalanced(`a}b`) {
		t.Fatal("多余右括号应判定为不平衡")
	}
}

// 复杂公式的集成检查：这类表达式在真实试卷里很常见。
func TestRenderComplexFormula(t *testing.T) {
	got := Render(`f(x) = \frac{1}{\sqrt{2\pi}\sigma} e^{-\frac{(x-\mu)^2}{2\sigma^2}}`)
	for _, want := range []string{"<m:f>", "<m:rad>", "π", "σ", "μ", "<m:sSup>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("复杂公式应包含 %s，实际 %s", want, got)
		}
	}
	if !IsBalanced(`f(x) = \frac{1}{\sqrt{2\pi}\sigma} e^{-\frac{(x-\mu)^2}{2\sigma^2}}`) {
		t.Fatal("该公式括号应当平衡")
	}
}
