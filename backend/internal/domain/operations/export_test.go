package operations

import "testing"

func TestSafeSpreadsheetCellNeutralizesFormulaInjection(t *testing.T) {
	// 攻击载荷：必须加前缀单引号强制按文本对待
	attacks := []string{
		"=1+1",
		"=cmd|'/c calc'!A1",
		"@SUM(1+9)*cmd|' /C calc'!A0",
		"+1+1",
		"-1+1",
		"=HYPERLINK(\"http://evil\",\"click\")",
	}
	for _, payload := range attacks {
		got := SafeSpreadsheetCell(payload)
		if got == payload {
			t.Fatalf("公式载荷未被中和：%q", payload)
		}
		if got[0] != '\'' {
			t.Fatalf("未以单引号开头：%q", got)
		}
	}
	// 纯数字（含正负号）是合法内容，不该被加前缀——加了前端就没法直接求和了
	for _, number := range []string{"0", "1500", "-1500", "+1500", "3.14", "-0.5", "1e6"} {
		if got := SafeSpreadsheetCell(number); got != number {
			t.Fatalf("数字 %q 被误处理成 %q", number, got)
		}
	}
	// 普通文本与空串原样返回
	for _, plain := range []string{"", "正常文本", " 空格开头", "abc=def", "1abc"} {
		if got := SafeSpreadsheetCell(plain); got != plain {
			t.Fatalf("普通文本 %q 被改成 %q", plain, got)
		}
	}
	// 危险字符只出现在首位时才需要处理，中间的 = 不会触发公式
	if got := SafeSpreadsheetCell("备注：金额=1500"); got != "备注：金额=1500" {
		t.Fatalf("中间含等号的文本被误处理：%q", got)
	}
}

func TestValidExportPeriodRequiresExactlyFromAndTo(t *testing.T) {
	good := map[string]any{"from": "2026-10-01", "to": "2026-10-10"}
	if !ValidExportPeriod(good) {
		t.Fatal("合法区间被判非法")
	}
	// 恰好 30 天是允许的上限
	if !ValidExportPeriod(map[string]any{"from": "2026-10-01", "to": "2026-10-31"}) {
		t.Fatal("30 天整应允许")
	}
	// 单日导出是正当需求，不该被当成非法区间
	if !ValidExportPeriod(map[string]any{"from": "2026-10-01", "to": "2026-10-01"}) {
		t.Fatal("单日区间应允许")
	}
	for name, filter := range map[string]map[string]any{
		"超过 30 天": {"from": "2026-10-01", "to": "2026-11-15"},
		"结束早于开始":  {"from": "2026-10-10", "to": "2026-10-01"},
		"缺 to":    {"from": "2026-10-01"},
		"缺 from":  {"to": "2026-10-01"},
		"多一个键":    {"from": "2026-10-01", "to": "2026-10-10", "status": "all"},
		"日期格式错":   {"from": "2026/10/01", "to": "2026/10/10"},
		"非字符串":    {"from": 20261001, "to": 20261010},
	} {
		if ValidExportPeriod(filter) {
			t.Fatalf("%s 应判非法", name)
		}
	}
}

func TestNextExportDateClosesRightOpenInterval(t *testing.T) {
	if got := NextExportDate("2026-10-01"); got != "2026-10-02" {
		t.Fatalf("结束日推一天失败：%s", got)
	}
	// 跨月与跨年都要正确
	if got := NextExportDate("2026-10-31"); got != "2026-11-01" {
		t.Fatalf("跨月失败：%s", got)
	}
	if got := NextExportDate("2026-12-31"); got != "2027-01-01" {
		t.Fatalf("跨年失败：%s", got)
	}
	// 闰年 2 月 28 日
	if got := NextExportDate("2028-02-28"); got != "2028-02-29" {
		t.Fatalf("闰日失败：%s", got)
	}
	// 非法输入原样返回，交由上层报错，不在这里猜
	if got := NextExportDate("不是日期"); got != "不是日期" {
		t.Fatalf("非法输入被改写：%s", got)
	}
}
