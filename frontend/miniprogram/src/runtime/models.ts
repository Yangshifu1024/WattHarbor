export interface UserSession {
  token: string;
  refresh_token: string;
  jwt: string;
  jwt_expires_in: number;
}

export interface UserLogin extends UserSession {
  user_id: string;
  is_new_user: boolean;
}

export interface ChargeUserProfile {
  user_id: string;
  nickname: string;
  avatar_url: string;
  phone_bound: boolean;
  registered_at: string;
  wallet: { status: string; available_cents: number; frozen_cents: number };
  coupon_unused_count: number;
}

// 进行中的充电。服务端无进行中订单时返回空串字段而不是 null；
// 横幅按 order_no 为空收起，不据此推断充电是否真的开始。
export interface OngoingCharge {
  order_no: string;
  status: string;
  device_id?: string;
  port_no?: number;
  started_at?: string | null;
  ended_at?: string | null;
}

// 结算账单行。金额单位分；read_at 为空即未读，已读时间不参与状态判断。
export interface BillRow {
  id: string;
  bill_no: string;
  order_no: string;
  device_id: string;
  total_cents: number;
  electric_cents: number;
  service_cents: number;
  issued_at: string;
  read_at: string | null;
}

// 支付前可用券的只读读模型；后端未提供用户领取端点，
// 也不返回优惠金额，页面不推算可省金额。
export interface RedeemableCoupon {
  grant_id: string;
  coupon_id: string;
  name: string;
  discount_type: 'amount' | 'percentage' | 'time_free';
  min_charge_cents: number;
  expires_at: string | null;
}
