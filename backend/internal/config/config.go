// Package config 加载运行配置。
//
// 优先级（高 → 低）：环境变量 > 配置文件(config.yaml) > 内置默认值。
// 刻意不引入 viper：手写加载逻辑更直观，答辩时能讲清楚每一行。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 全量配置。
type Config struct {
	App      AppConfig      `yaml:"app"`
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	JWT      JWTConfig      `yaml:"jwt"`
	Storage  StorageConfig  `yaml:"storage"`
	Log      LogConfig      `yaml:"log"`
	AI       AIConfig       `yaml:"ai"`
}

type AppConfig struct {
	Name string `yaml:"name"`
	// DataDir 运行期可写目录（数据库、上传文件、日志、向量索引都在其下）。
	// 桌面版指向 %APPDATA%\QuestionMind，服务器版指向 /data。
	DataDir string `yaml:"data_dir"`
	// SetupMode 由"是否已配置数据库"推导，不作为配置项暴露。
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	// Mode: debug | release
	Mode string `yaml:"mode"`
	// BaseURL 用于生成对外链接（如导出文件的下载地址）。
	BaseURL string `yaml:"base_url"`
	// CORSAllowOrigins 生产环境前后端同源，默认为空。
	CORSAllowOrigins []string `yaml:"cors_allow_origins"`
}

type DatabaseConfig struct {
	// Driver: sqlite | mysql
	Driver string `yaml:"driver"`
	// DSN 若为空则按 Driver 与 DataDir 自动推导（SQLite）。
	DSN string `yaml:"dsn"`
	// MaxOpenConns / MaxIdleConns 仅对 MySQL 生效。
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	// AutoMigrate 开发期自动建表；生产建议用版本化迁移。
	AutoMigrate bool `yaml:"auto_migrate"`
}

type JWTConfig struct {
	// Secret 为空时自动生成并持久化到 DataDir/secret.key，保证重启后 token 仍有效。
	Secret string `yaml:"secret"`
	Issuer string `yaml:"issuer"`
	// AccessTTL 访问令牌有效期。
	AccessTTL time.Duration `yaml:"access_ttl"`
}

type StorageConfig struct {
	// UploadDir / MediaDir 相对路径基于 DataDir 解析。
	UploadDir    string   `yaml:"upload_dir"`
	MediaDir     string   `yaml:"media_dir"`
	MaxUploadMB  int      `yaml:"max_upload_mb"`
	AllowedExts  []string `yaml:"allowed_exts"`
	PublicPrefix string   `yaml:"public_prefix"`
}

type LogConfig struct {
	Level string `yaml:"level"` // debug | info | warn | error
	// File 为空则只输出到 stdout；桌面版建议写入 DataDir/logs/app.log
	File string `yaml:"file"`
}

type AIConfig struct {
	// RequestTimeout 单次模型调用超时。
	RequestTimeout time.Duration `yaml:"request_timeout"`
	// MaxRetries 失败重试次数（指数退避）。
	MaxRetries int `yaml:"max_retries"`
	// AgentBudget 助手一次运行的预算。
	MaxTurns     int           `yaml:"max_turns"`
	MaxToolCalls int           `yaml:"max_tool_calls"`
	WallClock    time.Duration `yaml:"wall_clock"`
}

// Load 读取配置文件（若存在）并应用环境变量覆盖。
func Load(path string) (*Config, error) {
	cfg := defaultConfig()

	if path == "" {
		path = os.Getenv("QM_CONFIG")
	}
	if path == "" {
		path = "config.yaml"
	}
	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}

	applyEnv(cfg)
	if err := cfg.finalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaultConfig() *Config {
	return &Config{
		App: AppConfig{Name: "QuestionMind", DataDir: defaultDataDir()},
		Server: ServerConfig{
			Host: "127.0.0.1",
			Port: 8080,
			Mode: "debug",
		},
		Database: DatabaseConfig{
			Driver:          "sqlite",
			MaxOpenConns:    25,
			MaxIdleConns:    5,
			ConnMaxLifetime: time.Hour,
			AutoMigrate:     true,
		},
		JWT: JWTConfig{
			Issuer:    "questionmind",
			AccessTTL: 7 * 24 * time.Hour,
		},
		Storage: StorageConfig{
			UploadDir:    "uploads",
			MediaDir:     "media",
			MaxUploadMB:  50,
			PublicPrefix: "/media",
			AllowedExts: []string{
				".docx", ".doc", ".md", ".markdown", ".txt", ".pdf",
				".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp",
				".zip",
			},
		},
		Log: LogConfig{Level: "info"},
		AI: AIConfig{
			RequestTimeout: 120 * time.Second,
			MaxRetries:     2,
			MaxTurns:       5,
			MaxToolCalls:   20,
			WallClock:      180 * time.Second,
		},
	}
}

// defaultDataDir 返回运行期可写目录。
// 桌面版（由启动器设置 QM_DATA_DIR）与服务器版（容器内 /data）都通过环境变量覆盖。
func defaultDataDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".questionmind")
	}
	return "./data"
}

// applyEnv 用环境变量覆盖配置。命名规则：QM_<SECTION>_<FIELD>。
func applyEnv(cfg *Config) {
	setStr(&cfg.App.Name, "QM_APP_NAME")
	setStr(&cfg.App.DataDir, "QM_DATA_DIR")

	setStr(&cfg.Server.Host, "QM_HOST")
	setInt(&cfg.Server.Port, "QM_PORT")
	setStr(&cfg.Server.Mode, "QM_MODE")
	setStr(&cfg.Server.BaseURL, "QM_BASE_URL")
	if v := os.Getenv("QM_CORS_ALLOW_ORIGINS"); v != "" {
		cfg.Server.CORSAllowOrigins = splitAndTrim(v)
	}

	setStr(&cfg.Database.Driver, "QM_DB_DRIVER")
	setStr(&cfg.Database.DSN, "QM_DB_DSN")
	setBool(&cfg.Database.AutoMigrate, "QM_DB_AUTO_MIGRATE")
	// 兼容常见的 DATABASE_URL 写法
	if v := os.Getenv("DATABASE_URL"); v != "" && cfg.Database.DSN == "" {
		cfg.Database.DSN = v
	}

	setStr(&cfg.JWT.Secret, "QM_JWT_SECRET")
	setStr(&cfg.JWT.Issuer, "QM_JWT_ISSUER")
	setDuration(&cfg.JWT.AccessTTL, "QM_JWT_ACCESS_TTL")

	setStr(&cfg.Storage.UploadDir, "QM_UPLOAD_DIR")
	setStr(&cfg.Storage.MediaDir, "QM_MEDIA_DIR")
	setInt(&cfg.Storage.MaxUploadMB, "QM_MAX_UPLOAD_MB")

	setStr(&cfg.Log.Level, "QM_LOG_LEVEL")
	setStr(&cfg.Log.File, "QM_LOG_FILE")

	setDuration(&cfg.AI.RequestTimeout, "QM_AI_REQUEST_TIMEOUT")
	setInt(&cfg.AI.MaxRetries, "QM_AI_MAX_RETRIES")
	setInt(&cfg.AI.MaxTurns, "QM_AI_MAX_TURNS")
	setInt(&cfg.AI.MaxToolCalls, "QM_AI_MAX_TOOL_CALLS")
	setDuration(&cfg.AI.WallClock, "QM_AI_WALL_CLOCK")
}

// finalize 解析相对路径、创建必要目录、校验取值。
func (c *Config) finalize() error {
	if c.App.DataDir == "" {
		return fmt.Errorf("app.data_dir 不能为空")
	}
	abs, err := filepath.Abs(c.App.DataDir)
	if err != nil {
		return fmt.Errorf("解析 data_dir 失败: %w", err)
	}
	c.App.DataDir = abs

	if !filepath.IsAbs(c.Storage.UploadDir) {
		c.Storage.UploadDir = filepath.Join(c.App.DataDir, c.Storage.UploadDir)
	}
	if !filepath.IsAbs(c.Storage.MediaDir) {
		c.Storage.MediaDir = filepath.Join(c.App.DataDir, c.Storage.MediaDir)
	}
	if c.Log.File != "" && !filepath.IsAbs(c.Log.File) {
		c.Log.File = filepath.Join(c.App.DataDir, c.Log.File)
	}

	for _, dir := range []string{c.App.DataDir, c.Storage.UploadDir, c.Storage.MediaDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}

	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port 非法: %d", c.Server.Port)
	}
	if c.Database.Driver != "sqlite" && c.Database.Driver != "mysql" {
		return fmt.Errorf("database.driver 仅支持 sqlite / mysql，当前为 %q", c.Database.Driver)
	}
	if c.Database.Driver == "sqlite" && c.Database.DSN == "" {
		c.Database.DSN = "file:" + filepath.Join(c.App.DataDir, "questionmind.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	}
	if c.Database.Driver == "mysql" && c.Database.DSN == "" {
		return fmt.Errorf("database.driver=mysql 时必须提供 database.dsn（或设置 QM_DB_DSN）")
	}

	// JWT 密钥：未显式配置时自动生成并持久化，保证服务重启后已签发的
	// token 依然有效。绝不使用硬编码默认值——那等于没有签名。
	if c.JWT.Secret == "" {
		secret, err := loadOrCreateSecret(c.App.DataDir)
		if err != nil {
			return fmt.Errorf("初始化 JWT 密钥失败: %w", err)
		}
		c.JWT.Secret = secret
	}
	return nil
}

// SetAddr 覆盖监听地址。传入 "host:port"、"host" 或 ":port" 均可。
//
// 独立成一个函数而不是让调用方直接改 cfg.Server：
// 地址字符串的解析（IPv6 方括号、只给端口、只给主机）有几个容易出错的边界，
// 集中在一处处理更省心。空字符串表示不改动。
func SetAddr(c *Config, addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		// 没带冒号时按"只有主机"处理。
		if !strings.Contains(addr, ":") {
			c.Server.Host = addr
			return nil
		}
		return fmt.Errorf("解析监听地址 %q 失败: %w", addr, err)
	}

	if host != "" {
		c.Server.Host = host
	}
	if portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return fmt.Errorf("端口 %q 不是数字: %w", portStr, err)
		}
		if port < 1 || port > 65535 {
			return fmt.Errorf("端口 %d 超出范围 1-65535", port)
		}
		c.Server.Port = port
	}
	return nil
}

// LoadOverrides 加载配置并应用覆盖项，供命令行参数使用。
//
// 支持的键：server.addr / server.host / server.port / server.mode。
//
// 遇到不认识的键会**报错而不是忽略**。这一点是有教训的：
// 之前这里静默跳过了 "server.mode"，结果桌面版明明传了 release，
// gin 仍以 debug 模式启动，把整张路由表打到控制台上——
// 传了参数却没生效，比参数写错更难发现。
func LoadOverrides(path string, overrides map[string]string) (*Config, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	for key, value := range overrides {
		if value == "" {
			continue
		}
		switch key {
		case "server.addr", "addr":
			if err := SetAddr(cfg, value); err != nil {
				return nil, err
			}
		case "server.host":
			cfg.Server.Host = value
		case "server.port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("端口 %q 不是数字: %w", value, err)
			}
			cfg.Server.Port = port
		case "server.mode":
			if value != "debug" && value != "release" {
				return nil, fmt.Errorf("server.mode 只能是 debug 或 release，收到 %q", value)
			}
			cfg.Server.Mode = value
		default:
			return nil, fmt.Errorf("不支持的配置覆盖项 %q", key)
		}
	}
	return cfg, nil
}

// loadOrCreateSecret 从 <data_dir>/secret.key 读取密钥，不存在则生成。
// 文件权限设为 0600（仅属主可读写）。
func loadOrCreateSecret(dir string) (string, error) {
	path := filepath.Join(dir, "secret.key")
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s, nil
		}
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成随机密钥失败: %w", err)
	}
	secret := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		return "", fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	return secret, nil
}

// IsSQLite 便于后续分支处理方言差异。
func (c *Config) IsSQLite() bool { return c.Database.Driver == "sqlite" }

// Addr 返回监听地址。
func (c *Config) Addr() string { return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port) }

// IsProduction 是否生产模式。
func (c *Config) IsProduction() bool { return c.Server.Mode == "release" }

// ---- 环境变量辅助函数 ----

func setStr(dst *string, key string) {
	if v, ok := os.LookupEnv(key); ok {
		*dst = v
	}
}

func setInt(dst *int, key string) {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			*dst = n
		}
	}
}

func setBool(dst *bool, key string) {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			*dst = b
		}
	}
}

func setDuration(dst *time.Duration, key string) {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil {
			*dst = d
		}
	}
}

func splitAndTrim(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
