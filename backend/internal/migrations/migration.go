// Package migrations 提供版本化的数据库迁移机制。
//
// 为什么不用 GORM 的 AutoMigrate 就够了——它做不到三件事，而这三件事
// 恰恰是"能长期演进的系统"和"每次启动重跑一遍 DDL 的脚本"的区别：
//
//  1. 破坏性变更。AutoMigrate 只做加表、加列、加索引，
//     删列、改列名、改列类型它一律不做——而这恰恰是上线后最常见的需求。
//  2. 数据搬迁。字段拆分、值域回填、废弃数据清理，AutoMigrate 一概不管。
//  3. 可回答性。"这个库现在是什么结构"在 AutoMigrate 体系下无法回答：
//     没有版本记录，同一份代码在不同机器上可能停在不同的结构上，
//     而且没人能说清差异是什么时候、被谁造成的。
//
// 本包的做法（与 golang-migrate / Flyway / Alembic 同构，但按本项目需要裁剪）：
//
//   - 迁移是一个有序列表（见 registry.go），每条有唯一的 Version；
//   - 每条迁移在事务里执行，执行成功后写一行 schema_migrations 账本；
//   - 账本记录 (version, name, applied_at, checksum)，重跑是空操作；
//   - checksum 是迁移内容的哈希：**已经执行过的迁移被改动会被检测出来并拒绝执行**，
//     而不是像很多实现那样静默跳过。
//
// 关于"校验和能覆盖多少"的诚实说明：
// 运行中的二进制读不到源码，所以校验和只能覆盖迁移里**显式声明**的内容
// ——各方言的 SQL 脚本与 Body 描述。凡是用 Go 钩子（Up/Down）实现的行为，
// 作者必须同时修改 Body，让校验和随之变化。这一点在 Migration.Body 的
// 注释里又写了一遍，因为它是整个机制里唯一需要人自觉的地方。
//
// 为什么不用 golang-migrate/migrate 这类成熟库：引入它需要新增依赖，
// 而本项目的硬约束是"单二进制、CGO_ENABLED=0、离线可构建"。
// 这里的执行器只有几百行，语义也完全按本项目的双库需求裁剪过。
package migrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
)

// 方言名。与 gorm.io/driver 里 Dialector.Name() 的返回值保持一致。
const (
	DialectSQLite = "sqlite"
	DialectMySQL  = "mysql"

	// dialectAny 是 SQL 脚本表里的通配键：
	// 两种数据库语法一致的语句写在这里，个别方言再各自覆盖。
	dialectAny = "*"
)

// Migration 一条版本化迁移。
//
// 一条迁移由三部分组成，缺一不可：
//
//   - Body：人读的描述，同时也是校验和的输入之一；
//   - SQL / DownSQL：按方言给出的脚本，runner 会逐条执行；
//   - Up / Down：SQL 之外的 Go 钩子（少数迁移需要读写 Go 结构）。
type Migration struct {
	// Version 单调递增，从 1 开始。**一经发布不可更改、不可复用**：
	// 账本里存的就是这个数字，改了等于把所有部署的历史抹掉。
	Version int

	// Name 短标识，会原样写进账本并在 status 输出里显示。
	Name string

	// Body 是这条迁移的规范描述。
	//
	// 它参与校验和，因此**已经执行过的迁移一旦被改动就会被检测出来**。
	// 规矩只有一条：
	//
	//	如果改动落在这条迁移的 Go 钩子（Up/Down）里，
	//	必须同时改 Body ——否则校验和不变，改动会被静默放过。
	//
	// 之所以要靠人自觉：Go 运行时读不到函数体，无法自动算哈希。
	// 与其假装能自动检测，不如把这条规矩写明白。
	Body string

	// SQL 按方言给出正向脚本，值为多条以分号分隔的语句。
	// 键可以是具体方言名（"sqlite" / "mysql"），也可以是通配键 "*"。
	// 查找顺序：精确方言键 → 通配键。
	SQL map[string]string

	// DownSQL 回滚脚本，查找规则同上。为空表示没有可回滚的 SQL。
	DownSQL map[string]string

	// Up / Down 是可选钩子，在 SQL 之后（之前）于同一个连接上执行。
	// Up 为 nil 时只跑 SQL；Down 为 nil 时这条迁移不可回滚，
	// 此时应给出 NoDownReason，让 runner 能对用户说清楚为什么。
	Up   func(ctx context.Context, db *gorm.DB) error
	Down func(ctx context.Context, db *gorm.DB) error

	// NoDownReason 当 Down 与 DownSQL 都为空时，runner 报出的原因。
	NoDownReason string

	// NoTx 标记这条迁移必须脱离事务执行。
	//
	// 存在这个开关只有一个原因：SQLite 的破坏性变更必须走"整表重建"，
	// 而整表重建要求 PRAGMA foreign_keys=off —— 这个 PRAGMA 在事务内
	// 是**空操作**，写在事务里不会生效，于是 DROP TABLE 会被外键约束拒绝。
	// 详见 sqlite.go 里的 RebuildTable。
	//
	// 取这个开关的代价要说清楚：这条迁移的 DDL 不再由事务兜底，
	// 中途失败会留下半成品。runner 会为它单独记一笔账并打警告日志。
	NoTx bool

	// Tables 仅基线迁移使用：显式列出这条迁移负责建立的表。
	// 它的作用是**冻结基线**：模型可以继续长，但基线迁移不许跟着改，
	// 新表必须由新迁移负责。基线执行时会核对这张清单。
	Tables []string
}

// Label 形如 "0001_baseline"，用于日志与输出对齐。
func (m Migration) Label() string { return fmt.Sprintf("%04d_%s", m.Version, m.Name) }

// script 按方言取脚本，缺失时返回空串。
func (m Migration) script(dialect string) string { return pickScript(m.SQL, dialect) }

func (m Migration) downScript(dialect string) string { return pickScript(m.DownSQL, dialect) }

// pickScript 精确方言优先，其次通配键。
func pickScript(scripts map[string]string, dialect string) string {
	if len(scripts) == 0 {
		return ""
	}
	if s, ok := scripts[dialect]; ok {
		return s
	}
	return scripts[dialectAny]
}

// CanDown 当前方言下是否可回滚。
func (m Migration) CanDown(dialect string) bool {
	return m.Down != nil || strings.TrimSpace(m.downScript(dialect)) != ""
}

// Validate 在注册时校验迁移本身的合法性。
//
// 这些检查放在启动路径上做，是为了把"迁移写错了"变成一次明确的失败，
// 而不是等到某个部署的 DDL 跑到一半才炸。
func (m Migration) Validate() error {
	if m.Version <= 0 {
		return fmt.Errorf("迁移 %q 的版本号必须为正整数，当前为 %d", m.Name, m.Version)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("迁移 #%d 缺少名称", m.Version)
	}
	if strings.TrimSpace(m.Body) == "" {
		return fmt.Errorf("迁移 %s 缺少 Body：它是校验和的输入之一，缺了这条迁移就没有防篡改能力", m.Label())
	}
	if len(m.SQL) == 0 && m.Up == nil {
		return fmt.Errorf("迁移 %s 既没有 SQL 也没有 Up 钩子，什么都不会做", m.Label())
	}
	if m.Down == nil && m.downScript(DialectSQLite) == "" && m.downScript(DialectMySQL) == "" {
		if m.NoDownReason == "" {
			return fmt.Errorf("迁移 %s 不可回滚，请填写 NoDownReason 说明原因", m.Label())
		}
	}
	for dialect := range m.SQL {
		if dialect != dialectAny && dialect != DialectSQLite && dialect != DialectMySQL {
			return fmt.Errorf("迁移 %s 声明了未知方言 %q", m.Label(), dialect)
		}
	}
	return nil
}

// Checksum 计算迁移内容的哈希。
//
// 覆盖范围：版本、名称、Body、各方言的 SQL 与 DownSQL。
// 覆盖不到的是 Up/Down 两个 Go 函数体——原因见 Migration.Body 的注释。
//
// 归一化（换行符、行尾空白、方言键排序）不是洁癖：
// Windows 上 checkout 成 CRLF 会让同一份源码算出两个不同的校验和，
// 于是"我只是改了个换行"就能让所有 Windows 部署拒绝启动。
func (m Migration) Checksum() string {
	h := sha256.New()
	fmt.Fprintf(h, "questionmind-migration/v1\n")
	fmt.Fprintf(h, "version: %d\n", m.Version)
	fmt.Fprintf(h, "name: %s\n", m.Name)
	fmt.Fprintf(h, "body:\n%s\n", normalizeText(m.Body))
	fmt.Fprintf(h, "sql:\n%s\n", digestScripts(m.SQL))
	fmt.Fprintf(h, "down_sql:\n%s\n", digestScripts(m.DownSQL))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:])
}

// digestScripts 把脚本表按方言名排序后串起来，保证哈希与 map 迭代顺序无关。
func digestScripts(scripts map[string]string) string {
	keys := make([]string, 0, len(scripts))
	for k := range scripts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "[%s]\n%s\n", k, normalizeText(scripts[k]))
	}
	return b.String()
}

// normalizeText 统一换行并去掉行尾空白。
func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// --- SQL 语句切分 -------------------------------------------------------

// splitStatements 把脚本切成一条条可单独执行的语句。
//
// 为什么需要自己切：GORM 的 Exec 只接受单条语句，而迁移脚本天然是多条的。
// 用 strings.Split(script, ";") 切会在两种地方出错：
// 字符串字面量里的分号（'a;b'）和注释里的分号。
// 因此这里实现一个带状态的切分器，覆盖 -- 行注释、# 行注释（MySQL）、
// /* */ 块注释，以及单引号/双引号/反引号三种引用。
//
// 明确的限制：不支持存储过程/触发器（DELIMITER 语法）——
// 本项目的数据模型没有这类对象，与其写一个半吊子的解析器，
// 不如把限制写在明处。
func splitStatements(script string) ([]string, error) {
	var (
		out   []string
		buf   strings.Builder
		runes = []rune(normalizeText(script))
		i     = 0
	)
	flush := func() {
		if s := strings.TrimSpace(buf.String()); s != "" {
			out = append(out, s)
		}
		buf.Reset()
	}

	for i < len(runes) {
		c := runes[i]
		switch {
		case c == '-' && i+1 < len(runes) && runes[i+1] == '-':
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
		case c == '#':
			// MySQL 的行注释。SQLite 不支持 # 注释，但这里多切一次注释
			// 不会改变可执行语义，所以两种方言共用一个切分器。
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(runes) && runes[i+1] == '*':
			end := strings.Index(string(runes[i:]), "*/")
			if end < 0 {
				return nil, fmt.Errorf("SQL 脚本存在未闭合的块注释")
			}
			i += len([]rune(string(runes[i:])[:end])) + 2
		case c == '\'' || c == '"' || c == '`':
			quote := c
			buf.WriteRune(c)
			i++
			for i < len(runes) {
				if runes[i] == '\\' && quote != '`' && i+1 < len(runes) {
					// MySQL 默认允许反斜杠转义；SQLite 不允许，但同样无害。
					buf.WriteRune(runes[i])
					buf.WriteRune(runes[i+1])
					i += 2
					continue
				}
				buf.WriteRune(runes[i])
				if runes[i] == quote {
					// 连续两个引号是转义（'it''s'）
					if i+1 < len(runes) && runes[i+1] == quote {
						buf.WriteRune(runes[i+1])
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == ';':
			flush()
			i++
		default:
			buf.WriteRune(c)
			i++
		}
	}
	flush()

	if len(out) == 0 {
		return nil, fmt.Errorf("SQL 脚本为空")
	}
	return out, nil
}
