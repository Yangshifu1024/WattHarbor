package payment

import (
	"testing"

	"gorm.io/gorm"
	"time"
)

// TestEntryRejectedCoversEveryGuard 逐条验证支付回调的入口判定。
//
// 这段判断决定"什么回调可以被接受"：渠道、商户、应用、金额、openid、
// 时间窗任一不符都必须拒绝。误放行的后果是把一笔验签通过的回调结算到
// 别的订单上，钱和电都会错——所以每一项都要单独钉住。
//
// 判别依据是**返回值本身**，不依赖 gorm 行为。这一点是被迫学到的：
// 早先三版判据分别依赖"err != nil"、"不 panic"、DryRun 的查询/事务计数，
// 全部失败（0/6 有判别力）——因为 Apply 内部的事务在 DryRun 下根本不执行，
// 而事务体里的其他条件又返回同一个哨兵错误。抽出 entryRejected 之后，
// 判据不再需要经过 gorm，直接测就够。
func TestEntryRejectedCoversEveryGuard(t *testing.T) {
	now := time.Now().UTC()
	valid := VerifiedPayment{
		Provider: "wechat", MerchantID: "m1", AppID: "a1",
		MerchantOrderNo: "P123", TransactionID: "T1",
		OpenID: "o1", PaidCents: 100, PaidAt: now,
	}
	// DB 只需要非 nil：entryRejected 只判它存不存在，不使用它。
	// nil 的情况由 TestEntryRejectedNeedsDB 单独覆盖。
	base := CallbackService{
		DB: &gorm.DB{}, ExpectedProvider: "wechat", ExpectedMerchantID: "m1", ExpectedAppID: "a1",
	}

	cases := []struct {
		// name 当前边界场景名称，供 t.Run 区分断言；不作为业务身份或数据库条件。
		name string
		// mutate 覆写当前测试输入的回调，用于构造金额或已验证支付事实的非法边界；nil 时保留基准输入。
		mutate func(*VerifiedPayment)
		// store 覆写支付回调服务所期望渠道身份的可选回调；nil 时保留基准身份，不访问存储。
		store func(*CallbackService)
		// reject 预期入口是否拒绝当前已验证支付事实，只针对纯准入函数断言。
		reject bool
	}{
		{"基准：全部合规应放行", nil, nil, false},
		{"渠道不符", func(p *VerifiedPayment) { p.Provider = "other" }, nil, true},
		{"商户号不符", func(p *VerifiedPayment) { p.MerchantID = "other" }, nil, true},
		{"应用号不符", func(p *VerifiedPayment) { p.AppID = "other" }, nil, true},
		{"商户单号为空", func(p *VerifiedPayment) { p.MerchantOrderNo = "" }, nil, true},
		{"商户单号超长", func(p *VerifiedPayment) { p.MerchantOrderNo = longString(65) }, nil, true},
		{"商户单号恰好 64", func(p *VerifiedPayment) { p.MerchantOrderNo = longString(64) }, nil, false},
		{"渠道流水号为空", func(p *VerifiedPayment) { p.TransactionID = "" }, nil, true},
		{"渠道流水号超长", func(p *VerifiedPayment) { p.TransactionID = longString(65) }, nil, true},
		{"渠道流水号恰好 64", func(p *VerifiedPayment) { p.TransactionID = longString(64) }, nil, false},
		{"openid 为空", func(p *VerifiedPayment) { p.OpenID = "" }, nil, true},
		{"金额为零", func(p *VerifiedPayment) { p.PaidCents = 0 }, nil, true},
		{"金额为负", func(p *VerifiedPayment) { p.PaidCents = -1 }, nil, true},
		{"支付时间为零", func(p *VerifiedPayment) { p.PaidAt = time.Time{} }, nil, true},
		{"支付时间超前超过五分钟", func(p *VerifiedPayment) { p.PaidAt = now.Add(6 * time.Minute) }, nil, true},
		{"支付时间超前四分钟仍接受", func(p *VerifiedPayment) { p.PaidAt = now.Add(4 * time.Minute) }, nil, false},
		{"配置缺渠道", nil, func(s *CallbackService) { s.ExpectedProvider = "" }, true},
		{"配置缺商户号", nil, func(s *CallbackService) { s.ExpectedMerchantID = "" }, true},
		{"配置缺应用号", nil, func(s *CallbackService) { s.ExpectedAppID = "" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payment := valid
			store := base
			if tc.mutate != nil {
				tc.mutate(&payment)
			}
			if tc.store != nil {
				tc.store(&store)
			}
			paidAt := payment.PaidAt.UTC().Truncate(time.Millisecond)
			if got := store.entryRejected(payment, paidAt, now); got != tc.reject {
				t.Fatalf("期望 reject=%v，实际 %v", tc.reject, got)
			}
		})
	}
}

// TestEntryRejectedNeedsDB 单独覆盖 DB 句柄缺失这条——它在表驱动用例里
// 与其他条件耦合，拆出来测。
func TestEntryRejectedNeedsDB(t *testing.T) {
	now := time.Now().UTC()
	store := CallbackService{
		DB: &gorm.DB{}, ExpectedProvider: "wechat", ExpectedMerchantID: "m1", ExpectedAppID: "a1",
	}
	store.DB = nil
	payment := VerifiedPayment{
		Provider: "wechat", MerchantID: "m1", AppID: "a1",
		MerchantOrderNo: "P1", TransactionID: "T1",
		OpenID: "o1", PaidCents: 100, PaidAt: now,
	}
	if !store.entryRejected(payment, payment.PaidAt.UTC(), now) {
		t.Fatal("DB 为 nil 时应拒绝：否则会在下一行解引用空指针")
	}
}

// TestCallbackDigestDistinguishesIdentityFields 确认摘要只依赖业务身份。
// 渠道、商户、应用、openid、渠道单号、渠道流水号、金额任一变化，摘要必须变。
func TestCallbackDigestDistinguishesIdentityFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	base := VerifiedPayment{
		Provider: "wechat", MerchantID: "m1", AppID: "a1",
		MerchantOrderNo: "P123", TransactionID: "T1",
		OpenID: "o1", PaidCents: 100, PaidAt: now,
	}
	d0 := callbackDigest(base)

	for name, mutate := range map[string]func(*VerifiedPayment){
		"金额":     func(p *VerifiedPayment) { p.PaidCents++ },
		"渠道":     func(p *VerifiedPayment) { p.Provider = "other" },
		"商户号":    func(p *VerifiedPayment) { p.MerchantID = "m2" },
		"应用号":    func(p *VerifiedPayment) { p.AppID = "a2" },
		"商户单号":   func(p *VerifiedPayment) { p.MerchantOrderNo = "P124" },
		"渠道流水号":  func(p *VerifiedPayment) { p.TransactionID = "T2" },
		"openid": func(p *VerifiedPayment) { p.OpenID = "o2" },
	} {
		p := base
		mutate(&p)
		if callbackDigest(p) == d0 {
			t.Errorf("改 %s 后摘要未变，回调幂等判定会误判成重放", name)
		}
	}
	if callbackDigest(base) != d0 {
		t.Fatal("相同输入产生了不同摘要")
	}
}

// TestCallbackDigestIgnoresPaidAt 确认摘要**刻意不含支付时间**。
//
// 渠道重发同一笔通知时 PaidAt 会有微小差异（网关时钟、亚毫秒），而幂等判定
// 应当只依赖业务身份。含时间会把合法重放判成"同一流水号内容不一致"而报冲突，
// 资金就卡在回调里退不出去。
//
// 这条断言是写测试时才发现的：我最初断言"改时间摘要应变"，跑出来是 FAIL，
// 查实现才确认设计是对的、断言是错的。
func TestCallbackDigestIgnoresPaidAt(t *testing.T) {
	base := VerifiedPayment{
		Provider: "wechat", MerchantID: "m1", AppID: "a1",
		MerchantOrderNo: "P123", TransactionID: "T1",
		OpenID: "o1", PaidCents: 100, PaidAt: time.Now().UTC(),
	}
	for _, delta := range []time.Duration{
		time.Millisecond, time.Second, time.Minute, -time.Second,
	} {
		p := base
		p.PaidAt = base.PaidAt.Add(delta)
		if callbackDigest(p) != callbackDigest(base) {
			t.Errorf("支付时间偏移 %v 改变了摘要，合法重放会被判成内容冲突", delta)
		}
	}
}

// longString 接收非负字节长度 n，返回由 n 个 ASCII x 组成的字符串；零返回空串，负数由 make 触发 panic，无错误返回或外部 IO。
func longString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}
