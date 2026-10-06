package export

import (
	"context"
	"encoding/csv"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/xuri/excelize/v2"
	"io"
)

// FormatWriter 提供CSV标准转义和XLSX流式编码，不持有任务或文件路径。
type FormatWriter struct{}

// Write 在ctx下将headers及lines写入w，返回取消、格式或编码IO错误；不关闭调用方输出。
// 表格公式继续转义为文本，缺失字段由调用方映射为空串，格式化不会确认任务完成。
func (FormatWriter) Write(ctx context.Context, w io.Writer, format string, headers []string, lines [][]string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	w = contextWriter{ctx: ctx, output: w}
	switch format {
	case "csv":
		writer := csv.NewWriter(w)
		if err := writer.Write(headers); err != nil {
			return err
		}
		for _, line := range lines {
			if err := ctx.Err(); err != nil {
				return err
			}
			protected := make([]string, len(line))
			for i, value := range line {
				protected[i] = operations.SafeSpreadsheetCell(value)
			}
			if err := writer.Write(protected); err != nil {
				return err
			}
		}
		writer.Flush()
		return errors.Join(writer.Error(), ctx.Err())
	case "xlsx":
		book := excelize.NewFile()
		defer func() { err = errors.Join(err, book.Close()) }()
		stream, err := book.NewStreamWriter("Sheet1")
		if err != nil {
			return err
		}
		for row, line := range append([][]string{headers}, lines...) {
			if err := ctx.Err(); err != nil {
				return err
			}
			cells := make([]interface{}, len(line))
			for i, value := range line {
				cells[i] = operations.SafeSpreadsheetCell(value)
			}
			axis, err := excelize.CoordinatesToCellName(1, row+1)
			if err != nil {
				return err
			}
			if err := stream.SetRow(axis, cells); err != nil {
				return err
			}
		}
		if err := stream.Flush(); err != nil {
			return err
		}
		return errors.Join(book.Write(w), ctx.Err())
	default:
		return errors.New("unsupported export format")
	}
}

// contextWriter 在每次输出前响应ctx取消，不取得输出的关闭权。
type contextWriter struct {
	ctx    context.Context /* 本次同步编码请求 */
	output io.Writer       /* 调用方输出 */
}

// Write 将p写入原output，返回写入字节数或取消／IO错误，不再发起取消后的后续写入。
func (w contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.output.Write(p)
}
