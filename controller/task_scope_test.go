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
	prevRedisEnabled := common.RedisEnabled
	t.Cleanup(func() {
		model.DB = prevDB
		common.RedisEnabled = prevRedisEnabled
	})
	// role=100 分支会经 tasksToDto(items, true) -> model.GetUserCache 尝试读用户缓存；
	// 测试未初始化 Redis 客户端（common.RDB 为 nil），保持包默认的 RedisEnabled=true
	// 会在 RDB.HGetAll 上发生 nil 指针 panic。禁用 Redis 使其直接回退到数据库查询
	// （无 users 表时返回 err，被 tasksToDto 忽略，不影响断言）。
	common.RedisEnabled = false
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
