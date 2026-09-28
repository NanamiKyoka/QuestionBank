// Package omml 把 LaTeX 数学公式转换为 OMML（Office Math Markup Language）。
//
// 为什么需要自己实现：Word 的原生公式格式是 OMML，而不是 LaTeX。
// 若直接把 "$x^2$" 写进 docx，Word 只会显示成一串字面文本，公式排版全丢。
// Go 生态里没有可用的 LaTeX→OMML 库（Python 侧有 latex2mathml + mathml2omml，
// 但那是两段式且覆盖有限），因此这里实现一个针对中学/大学题库常见构造的转换器。
//
// 覆盖面：希腊字母、运算符与关系符、分式、根式、上下标、定界符、
// 大运算符（求和/积分/连乘）、函数名、文本。不追求完整 TeX 语义——
// 遇到不认识的命令会退化为字面文本，而不是报错失败，保证导出不会中断。
package omml

// symbol 一个数学符号：对应的 Unicode 字符，以及是否按"正体"排版。
type symbol struct {
	ch      string
	upright bool
}

// greek 希腊字母。
var greek = map[string]symbol{
	"alpha": {"α", false}, "beta": {"β", false}, "gamma": {"γ", false},
	"delta": {"δ", false}, "epsilon": {"ε", false}, "varepsilon": {"ε", false},
	"zeta": {"ζ", false}, "eta": {"η", false}, "theta": {"θ", false},
	"vartheta": {"ϑ", false}, "iota": {"ι", false}, "kappa": {"κ", false},
	"lambda": {"λ", false}, "mu": {"μ", false}, "nu": {"ν", false},
	"xi": {"ξ", false}, "pi": {"π", false}, "varpi": {"ϖ", false},
	"rho": {"ρ", false}, "varrho": {"ϱ", false}, "sigma": {"σ", false},
	"varsigma": {"ς", false}, "tau": {"τ", false}, "upsilon": {"υ", false},
	"phi": {"φ", false}, "varphi": {"φ", false}, "chi": {"χ", false},
	"psi": {"ψ", false}, "omega": {"ω", false},
	// 大写希腊字母在数学排版里默认是正体。
	"Gamma": {"Γ", true}, "Delta": {"Δ", true}, "Theta": {"Θ", true},
	"Lambda": {"Λ", true}, "Xi": {"Ξ", true}, "Pi": {"Π", true},
	"Sigma": {"Σ", true}, "Upsilon": {"Υ", true}, "Phi": {"Φ", true},
	"Psi": {"Ψ", true}, "Omega": {"Ω", true},
}

// operators 二元运算符与关系符。
var operators = map[string]symbol{
	"pm": {"±", false}, "mp": {"∓", false}, "times": {"×", false},
	"div": {"÷", false}, "cdot": {"⋅", false}, "ast": {"∗", false},
	"star": {"⋆", false}, "circ": {"∘", false}, "bullet": {"∙", false},
	"cap": {"∩", false}, "cup": {"∪", false}, "setminus": {"∖", false},
	"wedge": {"∧", false}, "vee": {"∨", false}, "oplus": {"⊕", false},
	"otimes": {"⊗", false}, "odot": {"⊙", false},

	"leq": {"≤", false}, "le": {"≤", false}, "geq": {"≥", false}, "ge": {"≥", false},
	"neq": {"≠", false}, "ne": {"≠", false}, "equiv": {"≡", false},
	"approx": {"≈", false}, "sim": {"∼", false}, "simeq": {"≃", false},
	"cong": {"≅", false}, "propto": {"∝", false},
	"in": {"∈", false}, "notin": {"∉", false}, "ni": {"∋", false},
	"subset": {"⊂", false}, "supset": {"⊃", false},
	"subseteq": {"⊆", false}, "supseteq": {"⊇", false},
	"forall": {"∀", false}, "exists": {"∃", false}, "nexists": {"∄", false},

	"to": {"→", false}, "rightarrow": {"→", false}, "leftarrow": {"←", false},
	"leftrightarrow": {"↔", false}, "Rightarrow": {"⇒", false},
	"Leftarrow": {"⇐", false}, "Leftrightarrow": {"⇔", false},
	"mapsto": {"↦", false}, "implies": {"⟹", false}, "iff": {"⟺", false},
	"uparrow": {"↑", false}, "downarrow": {"↓", false},

	"infty": {"∞", false}, "partial": {"∂", false}, "nabla": {"∇", false},
	"emptyset": {"∅", false}, "varnothing": {"∅", false},
	"angle": {"∠", false}, "perp": {"⊥", false}, "parallel": {"∥", false},
	"therefore": {"∴", false}, "because": {"∵", false},
	"dots": {"…", false}, "ldots": {"…", false}, "cdots": {"⋯", false},
	"vdots": {"⋮", false}, "ddots": {"⋱", false},
	"prime": {"′", false}, "degree": {"°", false},
	"triangle": {"△", false}, "square": {"□", false},
}

// delimiters \left/\right 支持的可伸缩定界符。
var delimiters = map[string]string{
	"(": "(", ")": ")", "[": "[", "]": "]",
	"lbrace": "{", "rbrace": "}",
	"langle": "⟨", "rangle": "⟩",
	"lceil": "⌈", "rceil": "⌉",
	"lfloor": "⌊", "rfloor": "⌋",
	"lvert": "|", "rvert": "|", "vert": "|",
	"lVert": "‖", "rVert": "‖", "Vert": "‖",
	".":         "", // \left. 表示不显示该侧定界符
	"backslash": "\\",
}

// naryOps 大运算符：上下限排在符号的上下方。
var naryOps = map[string]string{
	"sum": "∑", "prod": "∏", "coprod": "∐",
	"int": "∫", "iint": "∬", "iiint": "∭", "oint": "∮",
	"bigcup": "⋃", "bigcap": "⋂", "bigoplus": "⨁", "bigotimes": "⨂",
	"bigvee": "⋁", "bigwedge": "⋀",
}

// funcs 函数名：按正体排版，且其后紧跟的参数会作为"函数参数"包起来。
var funcs = map[string]string{
	"sin": "sin", "cos": "cos", "tan": "tan", "cot": "cot",
	"sec": "sec", "csc": "csc",
	"arcsin": "arcsin", "arccos": "arccos", "arctan": "arctan",
	"sinh": "sinh", "cosh": "cosh", "tanh": "tanh",
	"log": "log", "ln": "ln", "lg": "lg", "exp": "exp",
	"max": "max", "min": "min", "sup": "sup", "inf": "inf",
	"lim": "lim", "limsup": "lim sup", "liminf": "lim inf",
	"det": "det", "dim": "dim", "ker": "ker", "deg": "deg",
	"gcd": "gcd", "hom": "hom", "arg": "arg", "Pr": "Pr",
}

// accents 重音符号：OMML 用 m:acc 表示。
var accents = map[string]string{
	"hat": "̂", "widehat": "̂",
	"bar": "̄", "overline": "̄",
	"tilde": "̃", "widetilde": "̃",
	"dot": "̇", "ddot": "̈",
	"vec":   "⃗",
	"check": "̌", "breve": "̆",
	"acute": "́", "grave": "̀",
}

// textCommands 这些命令的内容按普通文本处理（正体、不解析数学）。
var textCommands = map[string]bool{
	"text": true, "textrm": true, "mathrm": true, "mbox": true,
	"operatorname": true, "textnormal": true,
}

// mathFonts 字体切换命令：解析其内容但忽略样式（导出以内容正确为优先）。
var mathFonts = map[string]bool{
	"mathbf": true, "mathit": true, "mathsf": true, "mathtt": true,
	"mathcal": true, "mathbb": true, "mathfrak": true, "boldsymbol": true,
}

// spacing 间距命令。OMML 用空格运行表示。
var spacing = map[string]string{
	",": " ", ";": " ", ":": " ", " ": " ",
	"quad": "  ", "qquad": "    ",
	"!": "", "thinspace": " ", "medspace": " ", "thickspace": " ",
}
