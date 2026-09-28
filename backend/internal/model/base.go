// Package model 定义 GORM 数据模型。
//
// 约定：
//   - 所有表继承 Base（或 SoftBase），统一 id / created_at / updated_at；
//   - 时间一律 UTC 存储（由 db 包的 NowFunc 保证），对外按 RFC3339 序列化；
//   - 结构化内容（富文本、选项、答案、排版节点）用 JSON 列存储，
//     MySQL 落原生 JSON，SQLite 落 TEXT，对业务代码透明。
package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Base 所有表的公共字段。
type Base struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SoftBase 需要软删除的表（题目、试卷等）。
type SoftBase struct {
	Base
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// JSON 跨方言 JSON 列。
//
// 之所以不直接用 gorm.io/datatypes：一是少一个依赖，二是可以显式控制
// 两种方言下的列类型（MySQL JSON / SQLite TEXT），这正是双数据库支持的关键点。
type JSON json.RawMessage

// Value 实现 driver.Valuer。
func (j JSON) Value() (driver.Value, error) {
	if len(j) == 0 {
		return nil, nil
	}
	return string(j), nil
}

// Scan 实现 sql.Scanner。
func (j *JSON) Scan(value any) error {
	if value == nil {
		*j = nil
		return nil
	}
	switch v := value.(type) {
	case []byte:
		*j = append((*j)[0:0], v...)
	case string:
		*j = append((*j)[0:0], v...)
	default:
		return fmt.Errorf("model.JSON: 不支持的扫描类型 %T", value)
	}
	return nil
}

// MarshalJSON 保证空值序列化为 null 而非 ""（空字符串会让前端 JSON.parse 失败）。
func (j JSON) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	return j, nil
}

// UnmarshalJSON 直接保留原始字节。
func (j *JSON) UnmarshalJSON(b []byte) error {
	if j == nil {
		return errors.New("model.JSON: UnmarshalJSON 接收者为 nil")
	}
	*j = append((*j)[0:0], b...)
	return nil
}

// GormDataType 供 GORM 推断 Go 侧类型。
func (JSON) GormDataType() string { return "json" }

// GormDBDataType 按方言选择列类型——双数据库支持的核心。
func (JSON) GormDBDataType(db *gorm.DB, _ *schema.Field) string {
	if db.Dialector.Name() == "mysql" {
		return "JSON"
	}
	return "TEXT"
}

// JSONOf 把一个 Go 值编码为 JSON 列。
func JSONOf(v any) (JSON, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("model.JSON: 编码失败: %w", err)
	}
	return JSON(b), nil
}

// MustJSONOf 编码失败返回 null，用于"尽力而为"的字段（如导入来源快照）。
func MustJSONOf(v any) JSON {
	j, err := JSONOf(v)
	if err != nil {
		return nil
	}
	return j
}

// Into 把 JSON 列反序列化到目标结构。空值不报错，保持目标零值。
func (j JSON) Into(target any) error {
	if len(j) == 0 {
		return nil
	}
	if err := json.Unmarshal(j, target); err != nil {
		return fmt.Errorf("model.JSON: 反序列化失败: %w", err)
	}
	return nil
}

// IsEmpty 判断是否为空 JSON。
func (j JSON) IsEmpty() bool { return len(j) == 0 || string(j) == "null" }

// String 便于日志输出。
func (j JSON) String() string {
	if len(j) == 0 {
		return "null"
	}
	return string(j)
}
