package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// PromptRepo 提示词模板数据访问。
type PromptRepo struct {
	db *gorm.DB
}

func NewPromptRepo(db *gorm.DB) *PromptRepo { return &PromptRepo{db: db} }

// List 返回全部模板行（全局与学科覆盖混在一起，按键、再按 id 排序）。
//
// 刻意不按学科过滤：管理界面需要"一屏看清所有覆盖"，
// 而覆盖的稀疏程度（多数学科没有覆盖）本身就是有用信息。
func (r *PromptRepo) List(ctx context.Context) ([]model.PromptTemplate, error) {
	var out []model.PromptTemplate
	err := conn(ctx, r.db).Order(keyOrder).Order(idOrder).Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("读取提示词模板失败: %w", err)
	}
	return out, nil
}

// 排序与条件一律走 clause，而不是裸字符串。
//
// 这一列的名字是 `key`，而 key 是 MySQL 的保留字：写在 Where/Order 里的
// 裸字符串不会被 GORM 加引号，在 MySQL 上就是一句语法错误，而 SQLite 上
// 却能跑通——这种"本地绿、线上炸"的差异最难查。交给 clause 生成
// 才既安全又跨方言。
var (
	keyOrder = clause.OrderByColumn{Column: clause.Column{Name: "key"}}
	idOrder  = clause.OrderByColumn{Column: clause.Column{Name: "id"}}
)

// keyCond 生成"按键取一行"的条件。
func keyCond(key string) clause.Expression {
	return clause.Eq{Column: clause.Column{Name: "key"}, Value: key}
}

// FindByKey 按精确键取一行；不存在返回 (nil, nil)。
//
// 返回 nil 而不是 ErrNotFound：调用方要区分"没配"（正常情况，走默认）
// 与"读失败"（异常，要报错），把它们混成一种错误会让降级逻辑写不对。
func (r *PromptRepo) FindByKey(ctx context.Context, key string) (*model.PromptTemplate, error) {
	var t model.PromptTemplate
	err := conn(ctx, r.db).Where(keyCond(key)).First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取提示词模板 %s 失败: %w", key, err)
	}
	return &t, nil
}

// Get 按 id 取一行。
func (r *PromptRepo) Get(ctx context.Context, id uint) (*model.PromptTemplate, error) {
	var t model.PromptTemplate
	err := conn(ctx, r.db).First(&t, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("提示词模板 %d 不存在", id)
		}
		return nil, fmt.Errorf("读取提示词模板 %d 失败: %w", id, err)
	}
	return &t, nil
}

// Create 新建一行。
func (r *PromptRepo) Create(ctx context.Context, t *model.PromptTemplate) error {
	if err := conn(ctx, r.db).Create(t).Error; err != nil {
		return fmt.Errorf("创建提示词模板失败: %w", err)
	}
	return nil
}

// UpdateFields 局部更新。
func (r *PromptRepo) UpdateFields(ctx context.Context, id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	if err := conn(ctx, r.db).Model(&model.PromptTemplate{}).Where("id = ?", id).Updates(fields).Error; err != nil {
		return fmt.Errorf("更新提示词模板 %d 失败: %w", id, err)
	}
	return nil
}

// Delete 硬删除一行。
//
// 刻意不走软删除：key 上有唯一索引，一条软删除的记录仍然占着这个键，
// 于是"删掉之后重新配同一个提示词"会被唯一索引挡下来，界面上表现为
// 保存报了个看不懂的错。提示词模板是配置而不是业务数据，
// 放进回收站也没有找回的价值，因此直接删干净。
func (r *PromptRepo) Delete(ctx context.Context, id uint) error {
	res := conn(ctx, r.db).Unscoped().Delete(&model.PromptTemplate{}, id)
	if res.Error != nil {
		return fmt.Errorf("删除提示词模板 %d 失败: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return apperr.ErrNotFound.WithDetail("提示词模板 %d 不存在", id)
	}
	return nil
}
