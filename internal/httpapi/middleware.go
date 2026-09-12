package httpapi

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"niche-service/internal/config"
)

const identityKey = "identity"

// Auth 校验 Bearer token，并把办理人员身份写入上下文。
func Auth(tokens map[string]config.Identity) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			c.AbortWithStatusJSON(401, gin.H{"error": "missing bearer token", "code": "unauthorized"})
			return
		}
		id, ok := tokens[strings.TrimPrefix(h, "Bearer ")]
		if !ok {
			c.AbortWithStatusJSON(401, gin.H{"error": "invalid token", "code": "unauthorized"})
			return
		}
		c.Set(identityKey, id)
		c.Next()
	}
}

// RequireRole 按办理角色限制敏感查询与操作。
func RequireRole(roles ...string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(c *gin.Context) {
		id := identityOf(c)
		if !allowed[id.Role] {
			c.AbortWithStatusJSON(403, gin.H{"error": "role not permitted", "code": "forbidden"})
			return
		}
		c.Next()
	}
}

func identityOf(c *gin.Context) config.Identity {
	v, ok := c.Get(identityKey)
	if !ok {
		return config.Identity{}
	}
	id, _ := v.(config.Identity)
	return id
}

// RedactingLogger 访问日志：只记录方法、路径、状态码、耗时与工号，
// 不记录请求体；路径中的证件号样式串一律脱敏，保证日志不泄露完整证件信息。
func RedactingLogger(w io.Writer) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		id := identityOf(c)
		fmt.Fprintf(w, "%s %s %d %s staff=%s ip=%s\n",
			c.Request.Method,
			Redact(c.Request.RequestURI),
			c.Writer.Status(),
			time.Since(start).Round(time.Microsecond),
			id.StaffID,
			c.ClientIP())
	}
}
