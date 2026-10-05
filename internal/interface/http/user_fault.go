package http

import (
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"strconv"
)

// reportFault 绑定 c 原用户与报修输入并调用所属完整事务，输出原自增主键／open或错误，不写表。
func (a UserAccountAPI) reportFault(c *gin.Context) {
	id, ok := a.userID(c)
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
			resourceWriteFailure(c, err)
		}
		return
	}
	httpapi.OK(c, out)
}

// myFaultReports 适配 c 原用户及分页并调用所属查询，输出原页或读取错误，无存储写入。
func (a UserAccountAPI) myFaultReports(c *gin.Context) {
	id, ok := a.userID(c)
	if !ok {
		return
	}
	page, size, ok := readPaging(c)
	if !ok {
		return
	}
	out, err := a.Faults.UserReports(c.Request.Context(), id, persist.PageQuery{Page: page, PageSize: size})
	if err != nil {
		httpapi.Write(c, 503, 5003, "报修记录暂时无法读取", nil)
		return
	}
	httpapi.OK(c, out)
}

// myFaultHistory 适配 c 原用户及路径后调用归属和可见性查询，不把数据库故障当作缺失报告。
func (a UserAccountAPI) myFaultHistory(c *gin.Context) {
	uid, ok := a.userID(c)
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
