package omml

// Expr 数学表达式的抽象语法树节点。
//
// 刻意保持很小的节点集合：OMML 能表达的结构虽然很多，但题库场景里
// 真正用到的就是这几种，多出来的节点只会增加维护面。
type Expr interface{ isExpr() }

// Row 一个表达式序列，是绝大多数节点的容器。
type Row struct {
	Items []Expr
}

// Run 一段字面文本。
//
// Upright 表示是否按正体排版：数学排版里变量用斜体、数字与运算符用正体，
// 所以 "2x" 会被解析成两个 Run（数字正体 + 变量斜体）。
type Run struct {
	Text    string
	Upright bool
}

// Frac 分式。
type Frac struct {
	Num Expr
	Den Expr
}

// Sqrt 根式。Deg 为 nil 表示平方根。
type Sqrt struct {
	Deg  Expr
	Base Expr
}

// Script 上下标。Sub/Sup 为 nil 表示没有该侧。
type Script struct {
	Base Expr
	Sub  Expr
	Sup  Expr
}

// Delim 定界符对，对应 \left( ... \right)。
type Delim struct {
	Beg  string
	End  string
	Body Expr
}

// Nary 大运算符（求和、积分、连乘等），上下限排在符号的上下方。
type Nary struct {
	Op   string
	Sub  Expr
	Sup  Expr
	Body Expr
}

// LimLow 把下标排在正下方的运算符，如 \lim_{x \to 0}。
type LimLow struct {
	Name string
	Sub  Expr
	Body Expr
}

// Func 函数名与其参数，如 \sin x。
type Func struct {
	Name string
	Arg  Expr
}

// Accent 重音符号，如 \vec{a}。
type Accent struct {
	Char string
	Base Expr
}

// Space 显式间距。
type Space struct {
	Width int
}

func (*Row) isExpr()    {}
func (*Run) isExpr()    {}
func (*Frac) isExpr()   {}
func (*Sqrt) isExpr()   {}
func (*Script) isExpr() {}
func (*Delim) isExpr()  {}
func (*Nary) isExpr()   {}
func (*LimLow) isExpr() {}
func (*Func) isExpr()   {}
func (*Accent) isExpr() {}
func (*Space) isExpr()  {}

// IsEmpty 判断序列是否没有任何有效内容。
func (r *Row) IsEmpty() bool {
	if r == nil {
		return true
	}
	for _, it := range r.Items {
		if run, ok := it.(*Run); ok && run.Text == "" {
			continue
		}
		if sp, ok := it.(*Space); ok && sp.Width == 0 {
			continue
		}
		return false
	}
	return true
}
