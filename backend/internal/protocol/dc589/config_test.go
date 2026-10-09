package dc589

import "testing"

// TestConfigAmountPrecision 验证下发只接受协议可表达的金额，合法值编解码保真；不扩展上报范围规则。
func TestConfigAmountPrecision(t *testing.T) {
	for _, cents := range []uint16{0, 10, 250, 500, 2500, 1, 255, 2499, 2510} {
		c := ConfigTable{CardAmountCents: cents, TemperatureGuard: 0xFF, FloatSeconds: 300, FloatDeciWatts: 100, RemoveSeconds: 60, TierRatioPercent: [5]byte{100, 100, 100, 100, 100}}
		payload, err := EncodeConfig(c)
		valid := cents <= 2500 && cents%10 == 0
		if (err == nil) != valid {
			t.Fatalf("cents=%d err=%v", cents, err)
		}
		if !valid {
			continue
		}
		decoded, err := DecodeConfig(Frame{Command: ConfigReport, Data: payload})
		if err != nil || decoded.CardAmountCents != cents {
			t.Fatalf("cents=%d decoded=%d err=%v", cents, decoded.CardAmountCents, err)
		}
	}
}
