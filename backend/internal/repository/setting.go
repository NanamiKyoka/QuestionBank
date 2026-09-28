package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// SettingRepo 系统设置数据访问。
type SettingRepo struct {
	db *gorm.DB
}

func NewSettingRepo(db *gorm.DB) *SettingRepo { return &SettingRepo{db: db} }

// Get 读取单个设置，不存在或为空时返回 def。
func (r *SettingRepo) Get(ctx context.Context, key, def string) string {
	var s model.SystemSetting
	err := conn(ctx, r.db).Where("setting_key = ?", key).First(&s).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return def
		}
		return def
	}
	if s.Value == "" {
		return def
	}
	return s.Value
}

func (r *SettingRepo) GetBool(ctx context.Context, key string, def bool) bool {
	switch r.Get(ctx, key, "") {
	case "":
		return def
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

func (r *SettingRepo) GetInt(ctx context.Context, key string, def int) int {
	v := r.Get(ctx, key, "")
	if v == "" {
		return def
	}
	n := 0
	for _, ch := range v {
		if ch < '0' || ch > '9' {
			return def
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// Set 写入单个设置。依赖 setting_key 唯一索引做 upsert，并发写入不会产生重复行。
func (r *SettingRepo) Set(ctx context.Context, key, value, description string, updatedBy uint) error {
	s := model.SystemSetting{
		Key:         key,
		Value:       value,
		Description: description,
		UpdatedBy:   updatedBy,
	}
	return conn(ctx, r.db).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "setting_key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "description", "updated_by", "updated_at"}),
		}).
		Create(&s).Error
}

func (r *SettingRepo) All(ctx context.Context) ([]model.SystemSetting, error) {
	var out []model.SystemSetting
	err := conn(ctx, r.db).Order("setting_key ASC").Find(&out).Error
	return out, err
}

func (r *SettingRepo) Map(ctx context.Context) (map[string]string, error) {
	list, err := r.All(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(list))
	for _, s := range list {
		m[s.Key] = s.Value
	}
	return m, nil
}

// ClaimOnce 原子地抢占一个键：只有第一个调用者返回 true。
//
// 利用唯一索引做 compare-and-set，比"先查再插"可靠——后者在并发下会全部通过。
// 用于初始化向导的并发保护等"只允许执行一次"的场景。
func (r *SettingRepo) ClaimOnce(ctx context.Context, key, value string) (bool, error) {
	s := model.SystemSetting{Key: key, Value: value, Description: "系统内部标记，请勿手工修改"}
	res := conn(ctx, r.db).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&s)
	if res.Error != nil {
		return false, fmt.Errorf("抢占标记 %s 失败: %w", key, res.Error)
	}
	return res.RowsAffected == 1, nil
}
