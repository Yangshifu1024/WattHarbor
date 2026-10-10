// Package identity 定义用户账号状态与性别取值词汇，供身份用例、在线卡、
// 运营后台与钱包用例共用。登录、鉴权与账号处置仍由所属用例负责，
// 本包不引用数据库模型、HTTP 框架或服务实现。
package identity

// UserStatus 是中央库 user.status 列的用户账号状态，用于登录准入与账号冻结恢复。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致（active/frozen）。
//
// 与 domain/wallet.AccountStatus 分立：两列取值串完全相同（active/frozen），
// 但分别描述用户账号与钱包账户。恢复钱包时同一行会同时判定两者，不得互相赋值。
type UserStatus string

const (
	// UserActive 表示账号正常可用。
	UserActive UserStatus = "active"
	// UserFrozen 表示账号已冻结，登录被拒绝。
	UserFrozen UserStatus = "frozen"
)

// Known 判断是否属于该列的数据库合法集合（两值）。
// 仅用于识别未知值；读取路径遇到未知值时保持原值透传，不得据此拒绝或替换。
func (s UserStatus) Known() bool {
	return s == UserActive || s == UserFrozen
}

// AllUserStatuses 是 UserStatus 的全量取值串，供入参白名单校验直接引用。
const AllUserStatuses = "active frozen"

// Gender 是中央库 user.gender 列的用户性别。取值与 migrations/central_db/0001_init.sql
// 中该 ENUM 列一致（unknown/male/female）。
type Gender string

const (
	// GenderUnknown 表示未登记性别，也是数据库默认值。
	GenderUnknown Gender = "unknown"
	// GenderMale 表示男性。
	GenderMale Gender = "male"
	// GenderFemale 表示女性。
	GenderFemale Gender = "female"
)

// Known 判断是否属于该列的数据库合法集合（三值）。
func (g Gender) Known() bool {
	switch g {
	case GenderUnknown, GenderMale, GenderFemale:
		return true
	default:
		return false
	}
}

// AllGenders 是 Gender 的全量取值串，供入参白名单校验直接引用。
const AllGenders = "unknown male female"
