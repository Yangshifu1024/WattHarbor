package device

import "testing"

func TestValidDeviceIDAcceptsTheApplicationRange(t *testing.T) {
	// 导入、网关开通、历史回填和计费规则共用该编号校验。
	for _, id := range []string{
		"DEV00001",                         // 恰好 8 位
		"DC589-0001",                       // 含短横线
		"dev_0001",                         // 含下划线
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ012345", // 恰好 32 位
	} {
		if !ValidDeviceID(id) {
			t.Fatalf("%q 应判合法", id)
		}
	}
}

func TestValidDeviceIDRejectsOutOfRangeAndBadCharset(t *testing.T) {
	for name, id := range map[string]string{
		"空串":      "",
		"不足 8 位":  "DEV0001",
		"只有 7 位":  "1234567",
		"超过 32 位": "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456",
		"含点":      "DEV.0001",
		"含中文":     "设备00001",
		"含空格":     "DEV 0001",
		"含冒号":     "DEV:0001",
	} {
		if ValidDeviceID(id) {
			t.Fatalf("%s（%q）应判非法", name, id)
		}
	}
}

func TestValidDeviceIDIsCaseInsensitive(t *testing.T) {
	// 编号判重大小写不敏感，但字符集校验对大小写都放行。
	if !ValidDeviceID("dev0001a") || !ValidDeviceID("DEV0001A") {
		t.Fatal("大小写都应合法")
	}
}
