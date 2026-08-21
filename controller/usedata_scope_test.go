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

// newQuotaDataScopeDB 建内存库并把 model.DB 指向它（quota_data 走主库 DB）。
func newQuotaDataScopeDB(t *testing.T) {
	t.Helper()
	prevDB := model.DB
	t.Cleanup(func() { model.DB = prevDB })
	db, err := gorm.Open(sqlite.Open("file:quotadatascope?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.QuotaData{}))
	// 两名用户同一模型、同一时间点，便于区分「全量聚合」与「按用户收窄」。
	// UseGroup 非空以满足 flow 查询的 use_group<>'' 条件；token/channel 置 0
	// 使 flow 的名称填充助手早退，无需 tokens/channels 表。
	require.NoError(t, db.Create(&model.QuotaData{UserID: 1, Username: "alice", ModelName: "gpt-4", UseGroup: "vip", CreatedAt: 1000, Count: 1, Quota: 100}).Error)
	require.NoError(t, db.Create(&model.QuotaData{UserID: 2, Username: "bob", ModelName: "gpt-4", UseGroup: "vip", CreatedAt: 1000, Count: 1, Quota: 200}).Error)
	t.Cleanup(func() { _ = db.Exec("DROP TABLE quota_data").Error })
	model.DB = db
}

func callGetAllQuotaDates(role, id int) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	// 时间跨度 < 1 个月，满足 GetUserQuotaDates 的跨度校验。
	c.Request = httptest.NewRequest(http.MethodGet, "/api/data?start_timestamp=1&end_timestamp=1000000", nil)
	c.Set("role", role)
	c.Set("id", id)
	GetAllQuotaDates(c)
	return rec
}

type quotaDatesResp struct {
	Success bool               `json:"success"`
	Data    []model.QuotaData `json:"data"`
}

func sumQuota(rows []model.QuotaData) int {
	total := 0
	for _, r := range rows {
		total += r.Quota
	}
	return total
}

func TestGetAllQuotaDatesIsolatesByUserForNonRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newQuotaDataScopeDB(t)

	// role=100：全量聚合，两人合计 300。
	var root quotaDatesResp
	rec := callGetAllQuotaDates(common.RoleRootUser, 1)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &root))
	assert.True(t, root.Success)
	assert.Equal(t, 300, sumQuota(root.Data))

	// role=10（id=1）：收窄为本人，仅 alice 的 100，且所有行 user_id=1。
	var admin quotaDatesResp
	rec = callGetAllQuotaDates(common.RoleAdminUser, 1)
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &admin))
	assert.Equal(t, 100, sumQuota(admin.Data))
	for _, r := range admin.Data {
		assert.Equal(t, 1, r.UserID)
	}

	// role=1（id=2）：仅 bob 的 200。
	var normal quotaDatesResp
	rec = callGetAllQuotaDates(common.RoleCommonUser, 2)
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &normal))
	assert.Equal(t, 200, sumQuota(normal.Data))
	for _, r := range normal.Data {
		assert.Equal(t, 2, r.UserID)
	}
}

func callGetAllFlowQuotaDates(role, id int) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/data/flow?start_timestamp=1&end_timestamp=1000000", nil)
	c.Set("role", role)
	c.Set("id", id)
	GetAllFlowQuotaDates(c)
	return rec
}

func TestGetAllFlowQuotaDatesIsolatesByUserForNonRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newQuotaDataScopeDB(t)

	type flowResp struct {
		Success bool                   `json:"success"`
		Data    []model.FlowQuotaData `json:"data"`
	}
	sumFlow := func(rows []model.FlowQuotaData) int {
		total := 0
		for _, r := range rows {
			total += r.Quota
		}
		return total
	}

	// role=100：全量，两人合计 300。
	var root flowResp
	rec := callGetAllFlowQuotaDates(common.RoleRootUser, 1)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &root))
	assert.True(t, root.Success)
	assert.Equal(t, 300, sumFlow(root.Data))

	// role=10（id=1）：委派 self，仅 alice 的 100。
	var admin flowResp
	rec = callGetAllFlowQuotaDates(common.RoleAdminUser, 1)
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &admin))
	assert.Equal(t, 100, sumFlow(admin.Data))
}

func callGetQuotaDatesByUser(role int) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/data/users?start_timestamp=1&end_timestamp=1000000", nil)
	c.Set("role", role)
	GetQuotaDatesByUser(c)
	return rec
}

func TestGetQuotaDatesByUserOnlyForRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newQuotaDataScopeDB(t)

	// role=100：跨用户「按用户对比」，两人合计 300。
	var root quotaDatesResp
	rec := callGetQuotaDatesByUser(common.RoleRootUser)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &root))
	assert.True(t, root.Success)
	assert.Equal(t, 300, sumQuota(root.Data))

	// role=10：非超管返回空集（前端亦隐藏该视图）。
	var admin quotaDatesResp
	rec = callGetQuotaDatesByUser(common.RoleAdminUser)
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &admin))
	assert.True(t, admin.Success)
	assert.Empty(t, admin.Data)
}
