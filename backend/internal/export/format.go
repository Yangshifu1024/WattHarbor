package export

import (
	"chargepilot/internal/domain/operations"
	"context"
	"encoding/csv"
	"errors"
	"github.com/xuri/excelize/v2"
	"io"
)

// FormatWriter 编码 CSV 和流式 XLSX，不管理任务或路径。
type FormatWriter struct{}

// Write 编码表头和行，公式转义为文本，缺字段由调用方填空串；不关闭输出，也不确认任务完成。
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

// contextWriter 在每次写入前检查取消，不负责关闭输出。
type contextWriter struct {
	ctx    context.Context /* 本次同步编码请求 */
	output io.Writer       /* 调用方输出 */
}

// Write 将字节写入输出，取消后不再写入。
func (w contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.output.Write(p)
}
