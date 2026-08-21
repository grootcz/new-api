# 使用日志按用户隔离 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 除超级管理员（role ≥ 100）外，任何人在「使用日志」页（消费/绘图/任务三种日志）只能看到自己的日志。

**Architecture:** 在三套「管理端」控制器（`GetAllLogs`/`GetLogsStat`/`GetAllMidjourney`/`GetAllTask`）顶部加统一判据：`role < RoleRootUser` 时委派给已存在的 self 处理器（按调用者自身 `user_id` 过滤）并返回；role ≥ 100 保持原逻辑不变。前端把日志页「管理员视图」门槛从 role ≥ 10 收紧到 role ≥ 100，作为双保险与正确 UX。

**Tech Stack:** Go 1.22 / Gin / GORM v2；测试用 `github.com/glebarez/sqlite` 内存库 + testify；前端 React 19 / TypeScript / Zustand（`useAuthStore`）。

## Global Constraints

- [强制] 所有回复、思考、注释使用中文。
- 后端测试使用 `github.com/stretchr/testify/require`（setup/致命断言）与 `assert`（非致命值检查）。
- 禁止直接 `encoding/json`；序列化走 `common.*`（本计划测试解析用 `common.Unmarshal`）。
- 三库（SQLite/MySQL/PostgreSQL）+ ClickHouse 日志库兼容；本方案 self 仅按 `user_id` 过滤，天然满足。
- 受保护标识 `new-api` / `QuantumNous` 不得改动。
- 角色常量：`common.RoleCommonUser=1`、`common.RoleAdminUser=10`、`common.RoleRootUser=100`；前端 `ROLE.ADMIN=10`、`ROLE.SUPER_ADMIN=100`。
- 三个后端 controller（`log.go`/`midjourney.go`/`task.go`）均已导入 `common`，无需新增 import。
- 响应体形状：`{success, message, data:{page, page_size, total, items}}`（`common.ApiSuccess` + `common.PageInfo`）。

---

### Task 1: 消费日志隔离（GetAllLogs + GetLogsStat）

**Files:**
- Create: `controller/log_scope_test.go`
- Modify: `controller/log.go`（`GetAllLogs` 顶部、`GetLogsStat` 顶部）

**Interfaces:**
- Consumes: 现有 self 处理器 `GetUserLogs(c *gin.Context)`（读 `c.GetInt("id")`）、`GetLogsSelfStat(c *gin.Context)`（读 `c.GetString("username")`）；常量 `common.RoleRootUser`。
- Produces: `GetAllLogs`/`GetLogsStat` 对 `role < 100` 的调用者收窄为 self（行为等价于对应 self 处理器）。

- [ ] **Step 1: 写失败测试**

创建 `controller/log_scope_test.go`：

```go
package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newLogScopeDB 建内存库并把 DB 与 LOG_DB 都指向它（GetAllLogs/GetUserLogs 走 LOG_DB）。
func newLogScopeDB(t *testing.T) {
	t.Helper()
	prevDB, prevLog := model.DB, model.LOG_DB
	t.Cleanup(func() { model.DB, model.LOG_DB = prevDB, prevLog })

	db, err := gorm.Open(sqlite.Open("file:logscope?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	// ChannelId=0 以跳过 channel 名称富化分支（避免依赖 channels 表）。
	require.NoError(t, db.Create(&model.Log{UserId: 1, Username: "alice", Type: model.LogTypeConsume, ModelName: "gpt-4", Quota: 100}).Error)
	require.NoError(t, db.Create(&model.Log{UserId: 2, Username: "bob", Type: model.LogTypeConsume, ModelName: "gpt-4", Quota: 200}).Error)
	t.Cleanup(func() { _ = db.Exec("DROP TABLE logs").Error })
	model.DB, model.LOG_DB = db, db
}

func callGetAllLogs(role, id int) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/log", nil)
	c.Set("role", role)
	c.Set("id", id)
	c.Set("username", map[int]string{1: "alice", 2: "bob"}[id])
	GetAllLogs(c)
	return rec
}

type logListResp struct {
	Success bool `json:"success"`
	Data    struct {
		Total int         `json:"total"`
		Items []model.Log `json:"items"`
	} `json:"data"`
}

func TestGetAllLogsIsolatesByUserForNonRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newLogScopeDB(t)

	// role=100：看全部
	var root logListResp
	rec := callGetAllLogs(common.RoleRootUser, 1)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &root))
	assert.True(t, root.Success)
	assert.Equal(t, 2, root.Data.Total)

	// role=10：只看自己（alice, id=1）
	var admin logListResp
	rec = callGetAllLogs(common.RoleAdminUser, 1)
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &admin))
	assert.Equal(t, 1, admin.Data.Total)
	require.Len(t, admin.Data.Items, 1)
	assert.Equal(t, 1, admin.Data.Items[0].UserId)

	// role=1：只看自己（bob, id=2）
	var common1 logListResp
	rec = callGetAllLogs(common.RoleCommonUser, 2)
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &common1))
	assert.Equal(t, 1, common1.Data.Total)
	require.Len(t, common1.Data.Items, 1)
	assert.Equal(t, 2, common1.Data.Items[0].UserId)
}

func TestGetLogsStatIsolatesByUserForNonRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newLogScopeDB(t)

	call := func(role, id int) map[string]any {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/log/stat", nil)
		c.Set("role", role)
		c.Set("id", id)
		c.Set("username", map[int]string{1: "alice", 2: "bob"}[id])
		GetLogsStat(c)
		var resp struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
		return resp.Data
	}

	// role=100：两人配额合计 300
	assert.EqualValues(t, 300, call(common.RoleRootUser, 1)["quota"])
	// role=10：仅 alice 的 100
	assert.EqualValues(t, 100, call(common.RoleAdminUser, 1)["quota"])
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./controller/ -run 'TestGetAllLogsIsolatesByUserForNonRoot|TestGetLogsStatIsolatesByUserForNonRoot' -v`
Expected: FAIL（role=10 返回 Total=2，断言 1 失败——隔离未生效）。

- [ ] **Step 3: 实现委派**

修改 `controller/log.go`，在 `GetAllLogs` 函数体第一行插入：

```go
func GetAllLogs(c *gin.Context) {
	if c.GetInt("role") < common.RoleRootUser {
		GetUserLogs(c)
		return
	}
	pageInfo := common.GetPageQuery(c)
	// …… 原有逻辑保持不变
```

在 `GetLogsStat` 函数体第一行插入：

```go
func GetLogsStat(c *gin.Context) {
	if c.GetInt("role") < common.RoleRootUser {
		GetLogsSelfStat(c)
		return
	}
	logType, _ := strconv.Atoi(c.Query("type"))
	// …… 原有逻辑保持不变
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./controller/ -run 'TestGetAllLogsIsolatesByUserForNonRoot|TestGetLogsStatIsolatesByUserForNonRoot' -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add controller/log.go controller/log_scope_test.go
git commit -m "feat: 消费日志按用户隔离(非超管收窄为self)"
```

---

### Task 2: 绘图日志隔离（GetAllMidjourney）

**Files:**
- Create: `controller/midjourney_scope_test.go`
- Modify: `controller/midjourney.go`（`GetAllMidjourney` 顶部）

**Interfaces:**
- Consumes: 现有 self 处理器 `GetUserMidjourney(c *gin.Context)`（读 `c.GetInt("id")`）；常量 `common.RoleRootUser`。
- Produces: `GetAllMidjourney` 对 `role < 100` 收窄为 self。

- [ ] **Step 1: 写失败测试**

创建 `controller/midjourney_scope_test.go`：

```go
package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newMjScopeDB(t *testing.T) {
	t.Helper()
	prevDB := model.DB
	t.Cleanup(func() { model.DB = prevDB })
	db, err := gorm.Open(sqlite.Open("file:mjscope?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Midjourney{}))
	require.NoError(t, db.Create(&model.Midjourney{UserId: 1, MjId: "m1", Action: "IMAGINE"}).Error)
	require.NoError(t, db.Create(&model.Midjourney{UserId: 2, MjId: "m2", Action: "IMAGINE"}).Error)
	t.Cleanup(func() { _ = db.Exec("DROP TABLE midjourneys").Error })
	model.DB = db
}

func callGetAllMidjourney(role, id int) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/mj", nil)
	c.Set("role", role)
	c.Set("id", id)
	GetAllMidjourney(c)
	return rec
}

func TestGetAllMidjourneyIsolatesByUserForNonRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newMjScopeDB(t)

	type resp struct {
		Data struct {
			Total int               `json:"total"`
			Items []model.Midjourney `json:"items"`
		} `json:"data"`
	}

	var root resp
	rec := callGetAllMidjourney(common.RoleRootUser, 1)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &root))
	assert.Equal(t, 2, root.Data.Total)

	var admin resp
	rec = callGetAllMidjourney(common.RoleAdminUser, 1)
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &admin))
	assert.Equal(t, 1, admin.Data.Total)
	require.Len(t, admin.Data.Items, 1)
	assert.Equal(t, 1, admin.Data.Items[0].UserId)
}
```

> 注：`&model.Midjourney{}` 的表名由 GORM 默认复数化为 `midjourneys`；若本地实际表名不同，以 `db.Migrator().CurrentDatabase()` 或运行时报错为准调整 DROP 语句（不影响断言）。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./controller/ -run TestGetAllMidjourneyIsolatesByUserForNonRoot -v`
Expected: FAIL（role=10 返回 Total=2）。

- [ ] **Step 3: 实现委派**

修改 `controller/midjourney.go`，在 `GetAllMidjourney` 函数体第一行插入：

```go
func GetAllMidjourney(c *gin.Context) {
	if c.GetInt("role") < common.RoleRootUser {
		GetUserMidjourney(c)
		return
	}
	pageInfo := common.GetPageQuery(c)
	// …… 原有逻辑保持不变
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./controller/ -run TestGetAllMidjourneyIsolatesByUserForNonRoot -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add controller/midjourney.go controller/midjourney_scope_test.go
git commit -m "feat: 绘图日志按用户隔离(非超管收窄为self)"
```

---

### Task 3: 任务日志隔离（GetAllTask）

**Files:**
- Create: `controller/task_scope_test.go`
- Modify: `controller/task.go`（`GetAllTask` 顶部）

**Interfaces:**
- Consumes: 现有 self 处理器 `GetUserTask(c *gin.Context)`（读 `c.GetInt("id")`）；常量 `common.RoleRootUser`。
- Produces: `GetAllTask` 对 `role < 100` 收窄为 self。

- [ ] **Step 1: 写失败测试**

创建 `controller/task_scope_test.go`：

```go
package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newTaskScopeDB(t *testing.T) {
	t.Helper()
	prevDB := model.DB
	t.Cleanup(func() { model.DB = prevDB })
	db, err := gorm.Open(sqlite.Open("file:taskscope?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Task{}))
	require.NoError(t, db.Create(&model.Task{TaskID: "t1", UserId: 1, Action: "song"}).Error)
	require.NoError(t, db.Create(&model.Task{TaskID: "t2", UserId: 2, Action: "song"}).Error)
	t.Cleanup(func() { _ = db.Exec("DROP TABLE tasks").Error })
	model.DB = db
}

func callGetAllTask(role, id int) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/task", nil)
	c.Set("role", role)
	c.Set("id", id)
	GetAllTask(c)
	return rec
}

func TestGetAllTaskIsolatesByUserForNonRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newTaskScopeDB(t)

	type resp struct {
		Data struct {
			Total int `json:"total"`
			Items []struct {
				UserId int `json:"user_id"`
			} `json:"items"`
		} `json:"data"`
	}

	var root resp
	rec := callGetAllTask(common.RoleRootUser, 1)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &root))
	assert.Equal(t, 2, root.Data.Total)

	var admin resp
	rec = callGetAllTask(common.RoleAdminUser, 1)
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &admin))
	assert.Equal(t, 1, admin.Data.Total)
	require.Len(t, admin.Data.Items, 1)
	assert.Equal(t, 1, admin.Data.Items[0].UserId)
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./controller/ -run TestGetAllTaskIsolatesByUserForNonRoot -v`
Expected: FAIL（role=10 返回 Total=2）。

- [ ] **Step 3: 实现委派**

修改 `controller/task.go`，在 `GetAllTask` 函数体第一行插入：

```go
func GetAllTask(c *gin.Context) {
	if c.GetInt("role") < common.RoleRootUser {
		GetUserTask(c)
		return
	}
	pageInfo := common.GetPageQuery(c)
	// …… 原有逻辑保持不变
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./controller/ -run TestGetAllTaskIsolatesByUserForNonRoot -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add controller/task.go controller/task_scope_test.go
git commit -m "feat: 任务日志按用户隔离(非超管收窄为self)"
```

---

### Task 4: 前端日志页视图收紧到超管（双保险 + 正确 UX）

**Files:**
- Modify: `web/src/hooks/use-admin.ts`（新增 `useIsRoot`）
- Modify: `web/src/features/usage-logs/components/usage-logs-provider.tsx`（`useLogsViewScope` 用 `useIsRoot`）

**Interfaces:**
- Consumes: `useAuthStore`、`ROLE.SUPER_ADMIN`。
- Produces: `useIsRoot(): boolean`（`user.role >= ROLE.SUPER_ADMIN`）；`useLogsViewScope().canManageScope` 变为「仅超管为 true」，使 role-10 在日志页走 self 接口并隐藏管理端列/筛选/视图切换。

- [ ] **Step 1: 新增 `useIsRoot` hook**

在 `web/src/hooks/use-admin.ts` 末尾追加（`ROLE`、`useAuthStore` 已在文件顶部导入）：

```ts
/**
 * Check if current user is a super admin (role >= 100)
 */
export function useIsRoot(): boolean {
  const { user } = useAuthStore((state) => state.auth)
  return (user?.role ?? 0) >= ROLE.SUPER_ADMIN
}
```

- [ ] **Step 2: 日志页作用域改用 `useIsRoot`**

修改 `web/src/features/usage-logs/components/usage-logs-provider.tsx`：

顶部 import 由：

```ts
import { useIsAdmin } from '@/hooks/use-admin'
```

改为：

```ts
import { useIsRoot } from '@/hooks/use-admin'
```

`useLogsViewScope` 内：

```ts
export function useLogsViewScope() {
  const canManageScope = useIsRoot()
  const { viewScope, setViewScope } = useUsageLogsContext()

  return {
    canManageScope,
    viewScope,
    setViewScope,
    isAdminView: canManageScope && viewScope === 'all',
  }
}
```

> 若 `useIsAdmin` 在本文件其他位置仍被使用，保留其 import；否则移除以免未使用告警。检查：`grep -n "useIsAdmin" web/src/features/usage-logs/components/usage-logs-provider.tsx`。

- [ ] **Step 3: 类型检查与构建**

Run（在 `web/` 下）：`bun run build`
Expected: 构建通过，无 TypeScript 错误、无未使用 import 告警。

- [ ] **Step 4: 手动验证（视觉）**

以 role-10 账号登录 → 打开「使用日志」页：三个 section 均为 user-view（无渠道列、无用户名筛选、无「全部/仅本人」切换），仅显示本人数据。以 role-100 账号登录 → 行为不变（可见全部与管理端筛选）。

- [ ] **Step 5: 提交**

```bash
git add web/src/hooks/use-admin.ts web/src/features/usage-logs/components/usage-logs-provider.tsx
git commit -m "feat(web): 使用日志页管理员视图收紧至超级管理员"
```

---

## 收尾验证（全部任务完成后）

- [ ] 后端全量测试：`go test ./controller/... -run 'Scope|Isolate' -v` 全绿。
- [ ] 编译：`go build ./...` 通过。
- [ ] 前端：`cd web && bun run build` 通过。
- [ ] 对照验收标准（见 spec 第 7 节）逐条确认。

## Self-Review 记录

- **Spec 覆盖**：规则表（Task 1-3 后端 + Task 4 前端）✓；三种日志列表+统计（Task 1 含 stat，Task 2/3 列表）✓；用户详情接口略过（spec 3.3，无任务，符合预期）✓；前端收紧（Task 4）✓。绘图/任务无独立 stat 卡片，故无 stat 任务，符合现状。
- **占位符扫描**：无 TBD/TODO；每个代码步骤均为可直接落地的完整代码。
- **类型一致性**：委派均调用已存在且签名一致的 self 处理器（`GetUserLogs`/`GetLogsSelfStat`/`GetUserMidjourney`/`GetUserTask`，均 `func(*gin.Context)`）；`common.RoleRootUser` 常量存在；前端 `useIsRoot` 与 `ROLE.SUPER_ADMIN` 一致。
