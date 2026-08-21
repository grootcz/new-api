# 使用日志按用户隔离 — 设计文档

- 日期：2026-08-21
- 范围：「使用日志」菜单（`/usage-logs/*`，含 common / drawing / task 三个 section）的全部数据面。
- 目标：除超级管理员外，任何人在使用日志页只能看到自己的日志。

## 1. 背景与现状

「使用日志」页有三个 section，分别对应三套后端接口，每套都有「管理端（看全部）」与「self（看本人）」两个变体：

| Section | 管理端接口（守卫） | self 接口（守卫） | 数据库 |
|---|---|---|---|
| 消费日志 common | `GET /api/log`（AdminAuth）→ `GetAllLogs`；`GET /api/log/stat`（AdminAuth）→ `GetLogsStat` | `GET /api/log/self` → `GetUserLogs`；`GET /api/log/self/stat` → `GetLogsSelfStat` | `LOG_DB`（可能独立库/ClickHouse）|
| 绘图日志 drawing | `GET /api/mj`（AdminAuth）→ `GetAllMidjourney` | `GET /api/mj/self` → `GetUserMidjourney` | `DB`（主库）|
| 任务日志 task | `GET /api/task`（AdminAuth）→ `GetAllTask` | `GET /api/task/self` → `GetUserTask` | `DB`（主库）|

现状问题：`AdminAuth()` 放行 `role >= 10`，因此**普通管理员（role 10）经管理端接口能看到全部用户的日志**，未做隔离。前端 `useLogsViewScope` 的 `canManageScope = useIsAdmin()`（`role >= 10`），使 role-10 进入「管理员视图」并调用管理端接口。

相关角色常量（`common/constants.go`）：`RoleCommonUser = 1`、`RoleAdminUser = 10`、`RoleRootUser = 100`。前端（`web/src/lib/roles.ts`）：`ROLE.ADMIN = 10`、`ROLE.SUPER_ADMIN = 100`。

## 2. 目标规则

「使用日志」页可见范围按角色：

| 角色 | role | 可见范围 |
|---|---|---|
| 超级管理员 RootUser | `>= 100` | 不受限，看全部（现状不变）|
| 普通管理员 AdminUser | `10` | 只看 `user_id = 自己`（与普通用户一致）|
| 普通用户 CommonUser | `1` | 只看自己（现状不变）|

统一判据：`role >= common.RoleRootUser` → 不受限；否则收窄为 self-only（按调用者自身 `user_id`）。适用于三种日志的**列表 + 统计 + 计数**全部数据面。

> 说明：本设计不涉及「用户分组 → 定价分组 → 渠道 → 模型」隔离链。经确认，日志隔离按纯用户维度（self）执行，普通管理员在日志页不再具有跨用户可见性。

## 3. 方案：控制器层委派（backend-enforced）

### 3.1 后端强制（主，必做）

在每个「管理端」控制器顶部加同一判据：非 root 调用者委派给对应的 self 处理器并返回。self 处理器已存在且按 `c.GetInt("id")` 过滤，直接复用。

改动点：

- `controller/log.go`
  - `GetAllLogs`：`if role < RoleRootUser { GetUserLogs(c); return }`
  - `GetLogsStat`：`if role < RoleRootUser { GetLogsSelfStat(c); return }`
- `controller/midjourney.go`
  - `GetAllMidjourney`：`if role < RoleRootUser { GetUserMidjourney(c); return }`
- `controller/task.go`
  - `GetAllTask`：`if role < RoleRootUser { GetUserTask(c); return }`

示例：

```go
func GetAllLogs(c *gin.Context) {
    if c.GetInt("role") < common.RoleRootUser {
        GetUserLogs(c) // self 版，按 c.GetInt("id") 过滤，等价于 /api/log/self
        return
    }
    // …… 原超管逻辑保持不变
}
```

要点：

- 路由守卫保持 `AdminAuth()` 不变（role-10 仍被授权进入分组，但控制器把其收窄为 self）。即便 role-10 直连管理端接口也拿不到他人数据。
- `role` 与 `id` 均已由认证中间件写入 gin context（现有 self 处理器即读取 `c.GetInt("id")`）。
- self-only 只按 `user_id` 过滤，**不触发 `LOG_DB`/ClickHouse 跨库子查询问题**，SQLite/MySQL/PostgreSQL/ClickHouse 天然兼容。
- role-10 传入的 `username`/`channel` 查询参数被 self 处理器忽略（本就 self-only，语义正确）。

### 3.2 前端视图（次，推荐 · 双保险 + 正确 UX）

把「使用日志」页的「管理员视图」门槛从 `role >= 10` 收紧到 `role >= 100`。

改动点：`web/src/features/usage-logs/components/usage-logs-provider.tsx` 中 `useLogsViewScope` 的 `canManageScope`，由 `useIsAdmin()`（≥10）改为基于 `ROLE.SUPER_ADMIN`（≥100）的判定。

- **仅新增一个日志页专用的 root 判定**（可在本文件内用 `useAuthStore` 读取 `user.role >= ROLE.SUPER_ADMIN`，或新增 `useIsRoot()` hook）；**不修改全局 `useIsAdmin`**，以免影响其他管理功能。
- 效果：role-10 在日志页 → `isAdminView = false` → 自动调用 `/api/log/self`、`/api/mj/self`、`/api/task/self`，并隐藏渠道列、用户名筛选、「全部/仅本人」视图切换。与后端形成双保险。

### 3.3 不在本次范围

- `/api/user/:id`（`GetUser`）：已有 `canManageTargetRole(myRole, user.Role)` 守卫，且被用户管理页复用。日志已 self-only 后，日志行点开永远是本人，用户详情弹窗不会泄漏他人。若改为 self-only 会误伤 role-10 的用户管理页。**本次不改此接口**（经确认略过）。
- 已废弃接口 `SearchAllLogs`/`SearchUserLogs` 保持不动。
- `/api/log/token`（`GetLogByKey`，令牌只读）本就按令牌 self，无需改动。
- 渠道管理菜单/页对普通用户的隔离由既有 `AdminAuth` 与上一提交（概览/关联管理面板隔离）保证，本次不重复。

## 4. 数据流

```
请求 → 认证中间件（写入 role, id 到 context）
     → 管理端控制器 GetAll*
        ├─ role >= 100：原逻辑，返回全量
        └─ role <  100：委派 GetUser*（按 id 过滤）→ 返回本人数据
```

前端（推荐改动后）：

```
role >= 100 → isAdminView=true  → 调 /api/log,  /api/mj,  /api/task
role <  100 → isAdminView=false → 调 /api/log/self, /api/mj/self, /api/task/self
```

## 5. 错误处理与 fail-safe

- 非 root 一律收窄为 self，无「空集放行 / 越权可见」路径。
- 委派复用现有 self 处理器，响应结构（`pageInfo{items,total}` / stat `{quota,rpm,tpm}`）与原 self 接口完全一致，前端无需适配额外形状。
- 认证缺失时由既有中间件拦截（`AdminAuth`/`UserAuth`），控制器内判据仅在已认证后生效。

## 6. 测试计划（Go，testify 表驱动）

后端控制器测试（seed 内存 DB + 构造 gin context 设定 `role`/`id`，遵循 `AGENTS.md` 后端测试质量要求）：

- **消费日志**：种子中写入两名用户的 `Log`。
  - `role=100`：`GetAllLogs` 返回两人全部行；`GetLogsStat` 统计含两人配额。
  - `role=10` 与 `role=1`：`GetAllLogs` 只返回本人行，他人行不可见；`GetLogsStat` 仅本人配额。
  - 断言 role-10 调 `GetAllLogs` 的结果等价于 `GetUserLogs`（同一 context）。
- **绘图日志**：种子两名用户的 `Midjourney`；`role=10` 经 `GetAllMidjourney` 只见本人；`role=100` 见全部。
- **任务日志**：种子两名用户的 `Task`；`role=10` 经 `GetAllTask` 只见本人；`role=100` 见全部。

前端（可选轻量）：断言 role-10 在使用日志页得到 user-view（渠道列不渲染、调用 self 接口）。

## 7. 验收标准

1. role-10 账号在「使用日志」页三个 section 均只能看到本人日志与本人统计；无法通过直连管理端接口（`/api/log`、`/api/mj`、`/api/task`、`/api/log/stat`）看到他人数据。
2. role-100 账号行为不变，仍看全部。
3. role-1 账号行为不变，仍只看本人。
4. 三库（SQLite/MySQL/PostgreSQL）及 ClickHouse 日志库下均正常。
5. 上述后端测试通过。
