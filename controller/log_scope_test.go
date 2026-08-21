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
