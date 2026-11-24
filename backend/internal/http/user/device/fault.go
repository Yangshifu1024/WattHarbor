package device

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"wattharbor/internal/device"
	userhttp "wattharbor/internal/http/user"
	"wattharbor/internal/persist"
	"wattharbor/internal/platform/httpapi"
)

// reportFault 提交当前用户的设备报修，返回工单编号和状态。
func (a API) reportFault(c *gin.Context) {
	id, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in device.FaultSubmission
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "报修请求无效")
		return
	}
	out, err := a.Faults.Report(c.Request.Context(), id, in)
	if err != nil {
		var input *device.FaultInputError
		if errors.As(err, &input) {
			httpapi.BadRequest(c, input.Message)
		} else {
			userhttp.WriteResourceFailure(c, err)
		}
		return
	}
	httpapi.OK(c, gin.H{"id": strconv.FormatUint(out.ID, 10), "status": out.Status})
}

// myFaultReports 分页返回当前用户的报修记录，编号转为十进制字符串。
func (a API) myFaultReports(c *gin.Context) {
	id, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	page, size, ok := httpapi.ReadPaging(c, 20, httpapi.DefaultPageLimit)
	if !ok {
		return
	}
	out, err := a.Faults.UserReports(c.Request.Context(), id, persist.PageQuery{Page: page, PageSize: size})
	if err != nil {
		httpapi.Write(c, 503, 5003, "报修记录暂时无法读取", nil)
		return
	}
	for _, row := range out.Items {
		row["id"] = fmt.Sprint(row["id"])
	}
	httpapi.OK(c, out)
}

// myFaultHistory 返回当前用户工单的可见处理记录；查询失败返回 503。
func (a API) myFaultHistory(c *gin.Context) {
	uid, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "报修编号无效")
		return
	}
	rows, err := a.Faults.UserHistory(c.Request.Context(), uid, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "报修记录不存在", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5003, "处理进度暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}
