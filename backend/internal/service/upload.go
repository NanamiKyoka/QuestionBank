package service

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"path/filepath"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/infra/storage"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// maxImagesPerRequest 一次请求最多上传几张图。
//
// 智能录题会把整页试卷拍照上传，几张到十几张是常见量，
// 因此不按"每次一张"限制；但也不能无限，否则一次请求就能把内存吃满。
const maxImagesPerRequest = 10

// UploadService 文件上传。
type UploadService struct {
	store        *storage.Local
	maxUploadMB  int
	maxImageEdge int
}

func NewUploadService(store *storage.Local, maxUploadMB int) *UploadService {
	if maxUploadMB <= 0 {
		maxUploadMB = 10
	}
	return &UploadService{
		store:        store,
		maxUploadMB:  maxUploadMB,
		maxImageEdge: storage.MaxImageWidth,
	}
}

// UploadImage 保存单张图片。
func (s *UploadService) UploadImage(_ context.Context, _ *model.User, file *multipart.FileHeader) (*SavedImage, error) {
	if file == nil {
		return nil, apperr.New(apperr.CodeInvalidParam, "缺少上传文件")
	}

	limit := int64(s.maxUploadMB) << 20
	// 先看声明的大小：Content-Length 可以被伪造，但能挡掉绝大多数误传大文件，
	// 省下读取到内存的代价。真正的兜底是下面的 LimitReader。
	if file.Size > limit {
		return nil, apperr.New(apperr.CodeInvalidParam,
			fmt.Sprintf("文件过大，上限 %d MB", s.maxUploadMB))
	}

	src, err := file.Open()
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidParam, "无法读取上传文件").WithCause(err)
	}
	defer func() { _ = src.Close() }()

	// 多读一个字节：读满 limit+1 就说明实际内容超出上限，
	// 说明声明的大小不可信。
	data, err := io.ReadAll(io.LimitReader(src, limit+1))
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidParam, "读取上传文件失败").WithCause(err)
	}
	if int64(len(data)) > limit {
		return nil, apperr.New(apperr.CodeInvalidParam,
			fmt.Sprintf("文件过大，上限 %d MB", s.maxUploadMB))
	}

	saved, err := s.store.Save(data, file.Filename)
	if err != nil {
		// 校验类错误（不是图片、损坏、尺寸过大）应作为 422 返回，
		// 让用户知道是文件本身的问题，而不是服务端出错。
		return nil, apperr.New(apperr.CodeInvalidParam, cleanStorageError(err)).WithCause(err)
	}

	return &SavedImage{
		URL:          saved.URL,
		Mime:         saved.Mime,
		Width:        saved.Width,
		Height:       saved.Height,
		Size:         saved.Size,
		OriginalName: saved.OriginalName,
	}, nil
}

// UploadImages 批量保存图片。
func (s *UploadService) UploadImages(ctx context.Context, user *model.User, files []*multipart.FileHeader) ([]SavedImage, error) {
	if len(files) == 0 {
		return nil, apperr.New(apperr.CodeInvalidParam, "没有可上传的文件")
	}
	if len(files) > maxImagesPerRequest {
		return nil, apperr.New(apperr.CodeInvalidParam,
			fmt.Sprintf("一次最多上传 %d 张图片", maxImagesPerRequest))
	}

	out := make([]SavedImage, 0, len(files))
	for _, file := range files {
		img, err := s.UploadImage(ctx, user, file)
		if err != nil {
			// 任何一张不合格就整体失败，并指出是哪一个。
			// 部分成功会让调用方拿到一半结果，还得自己判断哪张没传上去。
			return nil, apperr.From(err).WithDetail("文件 %s", filepath.Base(file.Filename))
		}
		out = append(out, *img)
	}
	return out, nil
}

// RemoveImage 删除媒体文件。
func (s *UploadService) RemoveImage(_ context.Context, _ *model.User, url string) error {
	if strings.TrimSpace(url) == "" {
		return apperr.New(apperr.CodeInvalidParam, "缺少地址")
	}
	// 只允许删除本服务媒体目录下的文件：否则传入任意外部地址
	// 都可能被当成路径去删磁盘上的东西。
	if !strings.HasPrefix(url, s.store.PublicPrefix()) {
		return apperr.New(apperr.CodeInvalidParam, "只能删除本系统上传的文件")
	}
	if err := s.store.Remove(url); err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	return nil
}

// SavedImage 上传结果。
type SavedImage struct {
	URL          string `json:"url"`
	Mime         string `json:"mime"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Size         int64  `json:"size"`
	OriginalName string `json:"original_name,omitempty"`
}

// cleanStorageError 去掉 storage 包的前缀，让文案直接面向用户。
func cleanStorageError(err error) string {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "storage: ")
	// 底层解码错误对用户没有意义，替换成一句话。
	if strings.Contains(msg, "图片已损坏或无法解析") {
		return "图片已损坏或无法解析"
	}
	return msg
}
