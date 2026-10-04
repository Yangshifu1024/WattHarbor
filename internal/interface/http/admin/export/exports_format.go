package export

import (
	"encoding/csv"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/xuri/excelize/v2"
	"io"
)

// writeExport 按格式把表头和数据行写出去：csv 走标准转义，xlsx 走流式写入避免大文件占内存，
// 其余格式一律返回错误。
func writeExport(w io.Writer, format string, headers []string, lines [][]string) error {
	switch format {
	case "csv":
		writer := csv.NewWriter(w)
		if err := writer.Write(headers); err != nil {
			return err
		}
		for _, line := range lines {
			protected := make([]string, len(line))
			for i, value := range line {
				protected[i] = operations.SafeSpreadsheetCell(value)
			}
			if err := writer.Write(protected); err != nil {
				return err
			}
		}
		writer.Flush()
		return writer.Error()
	case "xlsx":
		book := excelize.NewFile()
		defer book.Close()
		stream, err := book.NewStreamWriter("Sheet1")
		if err != nil {
			return err
		}
		for row, line := range append([][]string{headers}, lines...) {
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
		return book.Write(w)
	default:
		return errors.New("unsupported export format")
	}
}
