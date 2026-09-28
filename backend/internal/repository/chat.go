package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// ChatSessionRepo 对话会话数据访问。
type ChatSessionRepo struct {
	db *gorm.DB
}

func NewChatSessionRepo(db *gorm.DB) *ChatSessionRepo { return &ChatSessionRepo{db: db} }

func (r *ChatSessionRepo) Create(ctx context.Context, s *model.ChatSession) error {
	if err := conn(ctx, r.db).Create(s).Error; err != nil {
		return fmt.Errorf("创建会话失败: %w", err)
	}
	return nil
}

func (r *ChatSessionRepo) Get(ctx context.Context, id uint) (*model.ChatSession, error) {
	var s model.ChatSession
	if err := conn(ctx, r.db).First(&s, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("会话 %d 不存在", id)
		}
		return nil, err
	}
	return &s, nil
}

func (r *ChatSessionRepo) UpdateFields(ctx context.Context, id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return conn(ctx, r.db).Model(&model.ChatSession{}).Where("id = ?", id).Updates(fields).Error
}

func (r *ChatSessionRepo) SoftDelete(ctx context.Context, id uint) error {
	return conn(ctx, r.db).Delete(&model.ChatSession{}, id).Error
}

// ListByUser 返回用户的会话列表，最近活跃的在前。
func (r *ChatSessionRepo) ListByUser(ctx context.Context, userID uint, subjectID uint, limit, offset int) ([]model.ChatSession, int64, error) {
	q := conn(ctx, r.db).Model(&model.ChatSession{}).Where("user_id = ?", userID)
	if subjectID > 0 {
		q = q.Where("subject_id = ?", subjectID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var out []model.ChatSession
	err := q.Order("COALESCE(last_message_at, created_at) DESC, id DESC").
		Limit(limit).Offset(offset).Find(&out).Error
	return out, total, err
}

// Touch 更新会话的活跃时间与消息数。
func (r *ChatSessionRepo) Touch(ctx context.Context, id uint, at any, messageDelta int) error {
	return conn(ctx, r.db).Model(&model.ChatSession{}).Where("id = ?", id).
		Updates(map[string]any{
			"last_message_at": at,
			"message_count":   gorm.Expr("message_count + ?", messageDelta),
		}).Error
}

// ChatMessageRepo 对话消息数据访问。
type ChatMessageRepo struct {
	db *gorm.DB
}

func NewChatMessageRepo(db *gorm.DB) *ChatMessageRepo { return &ChatMessageRepo{db: db} }

func (r *ChatMessageRepo) Create(ctx context.Context, m *model.ChatMessage) error {
	if err := conn(ctx, r.db).Create(m).Error; err != nil {
		return fmt.Errorf("保存消息失败: %w", err)
	}
	return nil
}

// ListVisible 返回**可见**的会话消息。
//
// 过滤两类记录：
//   - role=tool 的工具结果，以及
//   - content 为空的 assistant 轮（纯工具调用轮，没有任何文字）
//
// 这两类对排障很有价值，但对用户没有意义——渲染进对话会让界面
// 出现一串天书。历史消息上限 200 条：更早的内容对当前对话帮助有限，
// 全量载入反而会挤占上下文。
func (r *ChatMessageRepo) ListVisible(ctx context.Context, sessionID uint, limit int) ([]model.ChatMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var out []model.ChatMessage
	err := conn(ctx, r.db).
		Where("session_id = ?", sessionID).
		Where("role <> ?", model.ChatRoleTool).
		Where("NOT (role = ? AND (content IS NULL OR content = ''))", model.ChatRoleAssistant).
		Order("id ASC").
		Limit(limit).
		Find(&out).Error
	return out, err
}

// RecentForContext 取最近若干条消息作为模型上下文（含工具消息）。
func (r *ChatMessageRepo) RecentForContext(ctx context.Context, sessionID uint, limit int) ([]model.ChatMessage, error) {
	var out []model.ChatMessage
	// 先按倒序取最近的，再翻转成正序——这样"最近 N 条"才准确。
	err := conn(ctx, r.db).
		Where("session_id = ?", sessionID).
		Order("id DESC").
		Limit(limit).
		Find(&out).Error
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (r *ChatMessageRepo) Get(ctx context.Context, id uint) (*model.ChatMessage, error) {
	var m model.ChatMessage
	if err := conn(ctx, r.db).First(&m, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("消息 %d 不存在", id)
		}
		return nil, err
	}
	return &m, nil
}

func (r *ChatMessageRepo) UpdateFields(ctx context.Context, id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return conn(ctx, r.db).Model(&model.ChatMessage{}).Where("id = ?", id).Updates(fields).Error
}

func (r *ChatMessageRepo) Delete(ctx context.Context, id uint) error {
	return conn(ctx, r.db).Delete(&model.ChatMessage{}, id).Error
}

// AgentRunRepo Agent 运行轨迹数据访问。同时实现 aiagent.RunStore。
type AgentRunRepo struct {
	db *gorm.DB
}

func NewAgentRunRepo(db *gorm.DB) *AgentRunRepo { return &AgentRunRepo{db: db} }

func (r *AgentRunRepo) StartRun(ctx context.Context, run *AgentRunRecord) error {
	m := &model.AgentRun{
		ID:           run.ID,
		SessionID:    run.SessionID,
		UserID:       run.UserID,
		SubjectID:    run.SubjectID,
		Surface:      run.Surface,
		Status:       model.AgentRunRunning,
		ModelID:      run.ModelID,
		ModelName:    run.ModelName,
		ProviderName: run.ProviderName,
	}
	return conn(ctx, r.db).Create(m).Error
}

// AgentRunRecord 运行记录的入参（避免 repository 依赖 aiagent 包）。
type AgentRunRecord struct {
	ID           string
	SessionID    *uint
	UserID       uint
	SubjectID    *uint
	Surface      string
	ModelID      *uint
	ModelName    string
	ProviderName string
}

// AgentStepRecord 步骤记录的入参。
type AgentStepRecord struct {
	RunID     string
	Idx       int
	StepType  string
	ToolName  string
	Args      any
	Result    any
	LatencyMs int
}

func (r *AgentRunRepo) AddStep(ctx context.Context, step *AgentStepRecord) error {
	m := &model.AgentStep{
		RunID:     step.RunID,
		Idx:       step.Idx,
		StepType:  step.StepType,
		ToolName:  step.ToolName,
		Args:      model.MustJSONOf(step.Args),
		Result:    model.MustJSONOf(step.Result),
		LatencyMs: step.LatencyMs,
	}
	return conn(ctx, r.db).Create(m).Error
}

func (r *AgentRunRepo) FinishRun(ctx context.Context, runID string, status model.AgentRunStatus, stopReason, errMsg string, usage ai.Usage) error {
	return conn(ctx, r.db).Model(&model.AgentRun{}).Where("id = ?", runID).
		Updates(map[string]any{
			"status":            status,
			"stop_reason":       stopReason,
			"error":             errMsg,
			"prompt_tokens":     usage.PromptTokens,
			"completion_tokens": usage.CompletionTokens,
		}).Error
}

// ListSteps 返回一次运行的步骤，供排障界面展示。
func (r *AgentRunRepo) ListSteps(ctx context.Context, runID string) ([]model.AgentStep, error) {
	var out []model.AgentStep
	err := conn(ctx, r.db).Where("run_id = ?", runID).Order("idx ASC").Find(&out).Error
	return out, err
}
