package schema

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain 关掉 gin 的 debug 噪音，测试输出只留用例本身。
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// bindJSON 按 handler 的方式校验请求体，保证测的是真实入口。
func bindJSON(t *testing.T, payload string, out any) error {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	return c.ShouldBindJSON(out)
}

// 新建供应商时，base_url 只认 http/https：file://、ftp:// 这类 scheme
// 必须在进门处挡下，否则要等到真正发起请求才会发现地址不能用。
func TestProviderCreateBaseURLBinding(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"", true},
		{"https://api.openai.com/v1", true},
		{"http://127.0.0.1:11434/v1", true},
		{"file:///etc/passwd", false},
		{"ftp://mirror.example.com/v1", false},
		{"api.openai.com/v1", false},
	} {
		payload := `{"name":"测试","interface_type":"openai","api_key":"sk-x","base_url":"` + tc.in + `"}`
		var req ProviderCreateRequest
		err := bindJSON(t, payload, &req)
		if (err == nil) != tc.want {
			t.Errorf("base_url=%q 校验结果 %v，期望通过=%v（err=%v）", tc.in, err == nil, tc.want, err)
		}
	}
}

// 超长地址在绑定层就要被拦下（max=255）。
func TestProviderCreateBaseURLMaxLength(t *testing.T) {
	payload := `{"name":"测试","interface_type":"openai","api_key":"sk-x","base_url":"` +
		strings.Repeat("a", 300) + `"}`
	var req ProviderCreateRequest
	if err := bindJSON(t, payload, &req); err == nil {
		t.Fatal("超过 max=255 的地址应当被拒绝")
	}
}

// 更新路径上的 BaseURL 是指针：omitempty 判的是"指针非 nil"，
// 因此这里只能挡长度，挡不住 scheme——scheme 由 service 层校验
// （见 service.validateBaseURL），这条用例把这个分工钉住。
func TestProviderUpdateBaseURLBinding(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    bool
	}{
		{"空串表示清空地址，绑定层不该拒绝", `{"base_url":""}`, true},
		{"scheme 交给 service 层，两条路径才共用一套规则", `{"base_url":"file:///etc/passwd"}`, true},
		{"长度仍然由绑定层把关", `{"base_url":"` + strings.Repeat("a", 300) + `"}`, false},
	} {
		var req ProviderUpdateRequest
		err := bindJSON(t, tc.payload, &req)
		if (err == nil) != tc.want {
			t.Errorf("%s：%s 的校验结果 %v，期望通过=%v（err=%v）", tc.name, tc.payload, err == nil, tc.want, err)
		}
	}
}
