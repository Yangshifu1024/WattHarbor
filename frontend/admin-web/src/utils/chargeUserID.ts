const MAX_USER_ID = '18446744073709551615';

/** 充电用户 ID 始终保留十进制字符串，避免超过 Number.MAX_SAFE_INTEGER 后丢失精度。 */
export function isChargeUserID(value: unknown): value is string {
  return typeof value === 'string' && /^[1-9]\d{0,19}$/.test(value)
    && (value.length < MAX_USER_ID.length || value <= MAX_USER_ID);
}
