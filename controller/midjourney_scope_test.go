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
			Total int                 `json:"total"`
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
